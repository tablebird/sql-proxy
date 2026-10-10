package backend

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sql-proxy/internal/config"
	"sql-proxy/internal/store"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// ------------------------------------------------------------------
// sqlpara 平台通用 API 响应结构
// ------------------------------------------------------------------

type apiResponse struct {
	Status int             `json:"status"`
	Msg    string          `json:"msg"`
	Data   json.RawMessage `json:"data"`
}

// ------------------------------------------------------------------
// sqlpara 平台 SQL 执行 / describe 返回的 data 字段结构
// ------------------------------------------------------------------

type sqlQueryData struct {
	FullSQL             string          `json:"full_sql"`
	IsExecute           bool            `json:"is_execute"`
	Checked             interface{}     `json:"checked"`
	IsMasked            bool            `json:"is_masked"`
	QueryTime           json.Number     `json:"query_time"`
	MaskRuleHit         bool            `json:"mask_rule_hit"`
	MaskTime            string          `json:"mask_time"`
	Warning             interface{}     `json:"warning"`
	ErrorMsg            *string         `json:"error"`
	ErrorCode           interface{}     `json:"error_code"`
	IsCritical          bool            `json:"is_critical"`
	ColumnList          []string        `json:"column_list"`
	ColumnType          []string        `json:"column_type"`
	Rows                [][]interface{} `json:"rows"`
	Status              interface{}     `json:"status"`
	AffectedRows        *int64          `json:"affected_rows"`
	LastInsertId        *int64          `json:"last_insert_id"`
	SecondsBehindMaster interface{}     `json:"seconds_behind_master"`
	PrivCheck           *bool           `json:"priv_check"`
}

type sqlCheckResponse struct {
	IsExecute    bool        `json:"is_execute"`
	Checked      string      `json:"checked"`
	Warning      interface{} `json:"warning"`
	Error        interface{} `json:"error"`
	WarningCount int64       `json:"warning_count"`
	ErrorCount   int64       `json:"error_count"`
	IsCritical   bool        `json:"is_critical"`
	SyntaxType   int64       `json:"syntax_type"`
	Rows         []checkRow  `json:"rows"`
	ColumnList   []string    `json:"column_list"`
	Status       interface{} `json:"status"`
	AffectedRows int64       `json:"affected_rows"`
}

type checkRow struct {
	ID                 int64  `json:"id"`
	Stage              string `json:"stage"`
	Errlevel           int64  `json:"errlevel"`
	Stagestatus        string `json:"stagestatus"`
	Errormessage       string `json:"errormessage"`
	SQL                string `json:"sql"`
	AffectedRows       int64  `json:"affected_rows"`
	Sequence           string `json:"sequence"`
	BackupDbname       string `json:"backup_dbname"`
	ExecuteTime        string `json:"execute_time"`
	Sqlsha1            string `json:"sqlsha1"`
	BackupTime         string `json:"backup_time"`
	ActualAffectedRows string `json:"actual_affected_rows"`
}

type sqlSubmitResponse struct {
	ID            int             `json:"id"`
	WorkflowID    int             `json:"workflow_id"`
	Workflow      json.RawMessage `json:"workflow"` // workflow原始json，动态结构
	SqlContent    json.RawMessage `json:"sql_content"`
	ReviewContent json.RawMessage `json:"review_content"`
	ExecuteResult json.RawMessage `json:"execute_result"`
}

type sqlParaClient struct {
	cfg         *config.BackendConfig
	httpClient  *http.Client
	cookieJar   *cookiejar.Jar
	cookieStore store.CookieStore

	mu            sync.Mutex
	authenticated bool
	authExpiresAt time.Time
}

func (c *sqlParaClient) Retries(ctx context.Context, fu func() (*SQLResponse, error)) (*SQLResponse, error) {
	var lastErr error
	var lastResp *SQLResponse
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
			logrus.Debugf("retry query, attempt %d/%d", attempt, c.cfg.MaxRetries)
		}

		if err := c.ensureAuthenticated(ctx); err != nil {
			return nil, err
		}

		resp, err := fu()
		if err != nil {
			lastErr = err
			lastResp = resp
			if isAuthErrorType(err) {
				c.invalidateAuth(ctx)
				continue
			}
			logrus.Warnf("request failed (attempt %d): %v", attempt+1, err)
			break
		}
		return resp, nil
	}

	if lastResp != nil {
		return lastResp, lastErr
	}
	return nil, fmt.Errorf("all retries failed: %w", lastErr)
}

func (c *sqlParaClient) Execute(ctx context.Context, req *SQLRequest) (*SQLResponse, error) {
	return c.Retries(ctx, func() (*SQLResponse, error) {
		return c.doSQLExecute(ctx, req)
	})
}

func (c *sqlParaClient) PreSubmit(ctx context.Context, req *SQLRequest) error {
	_, err := c.Retries(ctx, func() (*SQLResponse, error) {
		return nil, c.doSQLPreSubmit(ctx, req)
	})
	return err
}

func (c *sqlParaClient) Submit(ctx context.Context, req *SQLRequest) (*SQLResponse, error) {
	return c.Retries(ctx, func() (*SQLResponse, error) {
		return c.doSQLSubmit(ctx, req)
	})
}

func (c *sqlParaClient) doSQLPreSubmit(ctx context.Context, req *SQLRequest) error {
	sql := req.InterpolateSQL()

	metadata := req.Metadata
	if metadata == nil {
		return errors.New("metadata is empty")
	}

	db_name, dbOk := metadata["db_name"].(string)
	instance_id, IdOk := metadata["instance_id"].(string)
	if !dbOk || !IdOk || db_name == "" || instance_id == "" {
		return errors.New("[db/instance_id] is empty or type not string")
	}

	body := map[string]string{
		"db_name":     db_name,
		"instance_id": instance_id,
		"full_sql":    sql,
	}
	bytes, err := json.Marshal(body)
	if err != nil {
		return err
	}
	u := strings.TrimRight(c.cfg.BaseURL, "/") + "/api/v1/workflow/sqlcheck/"
	httpResp, respBody, err := c.doJsonPost(ctx, u, bytes)

	if err != nil {
		return err
	}
	_ = httpResp

	var apiResp sqlCheckResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return fmt.Errorf("parse response: %w, body: %s", err, truncateString(string(respBody), 500))
	}

	if apiResp.ErrorCount > 0 {
		errors := make([]string, 0, apiResp.ErrorCount)
		rows := apiResp.Rows
		for i := range rows {
			item := rows[i]
			if item.Errlevel > 1 {
				msg := fmt.Sprintf("[%s] %s", item.SQL, item.Errormessage)
				errors = append(errors, msg)
			}
		}
		return fmt.Errorf("SQL syntax error: %s", strings.Join(errors, "\n"))
	}
	return nil
}

func (c *sqlParaClient) doSQLSubmit(ctx context.Context, req *SQLRequest) (*SQLResponse, error) {
	sql := req.InterpolateSQL()

	metadata := req.Metadata
	if metadata == nil {
		return nil, errors.New("metadata is empty")
	}
	db_name, dbOk := metadata["db_name"].(string)
	instance_id, IdOk := metadata["instance_id"].(string)
	workflow_name, workflowOk := metadata["workflow_name"].(string)
	group_id, groupOk := metadata["group_id"].(string)
	is_backup, _ := metadata["is_backup"].(bool)
	run_date_start, _ := metadata["run_date_start"].(string)
	run_date_end, _ := metadata["run_date_end"].(string)
	if !dbOk || !IdOk || !workflowOk || !groupOk || db_name == "" || instance_id == "" || workflow_name == "" || group_id == "" {
		return nil, errors.New("[db/instance_id/workflow_name/group_id] is empty or type not string")
	}
	workflow := map[string]any{
		"workflow_name":  workflow_name,
		"group_id":       group_id,
		"db_name":        db_name,
		"is_backup":      is_backup,
		"instance":       instance_id,
		"run_date_start": run_date_start,
		"run_date_end":   run_date_end,
	}
	body := map[string]any{
		"workflow":    workflow,
		"sql_content": sql,
	}

	bytes, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	u := strings.TrimRight(c.cfg.BaseURL, "/") + "/api/v1/workflow/"
	httpResp, respBody, err := c.doJsonPost(ctx, u, bytes)

	if err != nil {
		return nil, err
	}

	if httpResp.StatusCode != 201 {
		return nil, fmt.Errorf("status code: %d", httpResp.StatusCode)
	}

	var resp sqlSubmitResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w, body: %s", err, truncateString(string(respBody), 500))
	}

	return &SQLResponse{
		Success: true,
		Message: string(resp.Workflow),
	}, nil
}

// ------------------------------------------------------------------
// 核心：SQL 执行（与接口 5 同结构）
// ------------------------------------------------------------------

func (c *sqlParaClient) doSQLExecute(ctx context.Context, req *SQLRequest) (*SQLResponse, error) {
	sql := req.InterpolateSQL()

	form := url.Values{}
	form.Set("instance_name", req.InstanceName)
	form.Set("db_name", req.Database)
	form.Set("schema_name", "")
	form.Set("tb_name", "")
	form.Set("sql_content", sql)

	u := strings.TrimRight(c.cfg.BaseURL, "/") + "/" + "query/"
	logrus.Debugf("sending SQL to %s: %s", u, truncateString(sql, 200))

	httpResp, respBody, err := c.doFormPost(ctx, u, form)
	if err != nil {
		return nil, err
	}
	_ = httpResp

	var apiResp apiResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("parse response: %w, body: %s", err, truncateString(string(respBody), 500))
	}
	if apiResp.Status != 0 {
		return &SQLResponse{Success: false, Message: apiResp.Msg},
			fmt.Errorf("sqlpara api status=%d msg=%s", apiResp.Status, apiResp.Msg)
	}

	var qd sqlQueryData
	if err := json.Unmarshal(apiResp.Data, &qd); err == nil {
		return c.convertQueryData(&qd), nil
	} else {
		logrus.Warnf("parse query data fallback: %v", err)
	}

	var rawRows []map[string]interface{}
	if err := json.Unmarshal(apiResp.Data, &rawRows); err == nil && len(rawRows) > 0 {
		return convertRawMapRows(rawRows), nil
	}

	var anyData interface{}
	_ = json.Unmarshal(apiResp.Data, &anyData)
	return &SQLResponse{
		Success: true,
		Results: []*QueryResult{{
			Columns: []string{"result"},
			Types:   []string{"VAR_STRING"},
			Rows:    [][]interface{}{{fmt.Sprintf("%v", anyData)}},
		}},
	}, nil
}

func (c *sqlParaClient) convertQueryData(qd *sqlQueryData) *SQLResponse {
	resp := &SQLResponse{Success: true}

	// 错误判断
	if qd.ErrorMsg != nil && *qd.ErrorMsg != "" {
		resp.Success = false
		resp.Message = *qd.ErrorMsg
	}

	// 结果集
	if len(qd.ColumnList) > 0 || len(qd.Rows) > 0 {
		resp.Results = []*QueryResult{{
			Columns: qd.ColumnList,
			Types:   qd.ColumnType,
			Rows:    qd.Rows,
		}}
	}

	// 行数
	if qd.AffectedRows != nil {
		resp.RowCount = *qd.AffectedRows
	}
	if qd.LastInsertId != nil {
		resp.LastInsertID = *qd.LastInsertId
	}

	return resp
}

func convertRawMapRows(rows []map[string]interface{}) *SQLResponse {
	if len(rows) == 0 {
		return &SQLResponse{Success: true}
	}
	columns := make([]string, 0, len(rows[0]))
	for k := range rows[0] {
		columns = append(columns, k)
	}
	result := make([][]interface{}, 0, len(rows))
	for _, r := range rows {
		row := make([]interface{}, len(columns))
		for i, col := range columns {
			row[i] = r[col]
		}
		result = append(result, row)
	}
	types := make([]string, len(columns))
	for i := range types {
		types[i] = "VAR_STRING"
	}
	return &SQLResponse{
		Success: true,
		Results: []*QueryResult{{
			Columns: columns,
			Types:   types,
			Rows:    result,
		}},
	}
}

// ------------------------------------------------------------------
// 认证
// ------------------------------------------------------------------

func (c *sqlParaClient) authenticate(ctx context.Context) error {

	if _, _, err := c.doGetAllowHTML(ctx, c.cfg.BaseURL+"/login/"); err != nil {
		return fmt.Errorf("get login page failed: %w", err)
	}

	csrfToken := c.getCsrfToken()
	if csrfToken == "" {
		logrus.Debug("no CSRF token found from login page, proceeding without X-CSRFToken header")
	}

	u := strings.TrimRight(c.cfg.BaseURL, "/") + "/authenticate/"

	form := url.Values{}
	form.Set("username", c.cfg.Username)
	form.Set("password", c.cfg.Password)

	httpResp, body, err := c.doFormPostAllowHTML(ctx, u, form)
	if err != nil {
		if c.hasSessionCookie() {
			logrus.Warnf("authenticate returned error but session cookie is present: %v", err)
		} else {
			if body != nil {
				logrus.Errorf("authenticate body: %s", body)
				return fmt.Errorf("authenticate returned login page HTML, check username/password or headers")
			}
			return fmt.Errorf("authenticate failed: %w", err)
		}
	}
	_ = httpResp

	// If the response body is JSON, validate the API status.
	if len(body) > 0 {
		var apiResp apiResponse
		if jsonErr := json.Unmarshal(body, &apiResp); jsonErr == nil {
			if apiResp.Status != 0 {
				return fmt.Errorf("authenticate api status=%d msg=%s", apiResp.Status, apiResp.Msg)
			}
		}
	}

	// 即使 body 不是 JSON（常见是 302 重定向到首页，body 为 HTML），只要 cookie 已设置就认为登录可能成功
	if !c.hasSessionCookie() {
		return fmt.Errorf("authenticate did not set session cookie, check credentials or CSRF headers")
	}

	if err := c.saveCookies(ctx); err != nil {
		logrus.Warnf("save sqlpara cookies to store failed: %v", err)
	}

	c.mu.Lock()
	c.authenticated = true
	c.authExpiresAt = time.Now().Add(c.cfg.AuthReuseInterval)
	c.mu.Unlock()
	logrus.Infof("authenticated to sqlpara as %s", c.cfg.Username)
	return nil
}

func (c *sqlParaClient) cookieStoreKey() string {
	sum := sha256.Sum256([]byte(c.cfg.BaseURL + "\x00" + c.cfg.Username))
	return "sql-proxy:sqlpara:cookie:" + hex.EncodeToString(sum[:])
}

func (c *sqlParaClient) saveCookies(ctx context.Context) error {
	if c.cookieStore == nil {
		return fmt.Errorf("cookieStore is nil")
	}

	u, err := url.Parse(c.cfg.BaseURL)
	if err != nil {
		return err
	}
	data, err := json.Marshal(c.cookieJar.Cookies(u))
	if err != nil {
		return err
	}
	return c.cookieStore.Set(ctx, c.cookieStoreKey(), data, c.cfg.AuthReuseInterval)
}

func (c *sqlParaClient) restoreCookies(ctx context.Context) (bool, error) {
	if c.cookieStore == nil {
		return false, fmt.Errorf("cookieStore is nil")
	}

	data, found, err := c.cookieStore.Get(ctx, c.cookieStoreKey())
	if err != nil || !found {
		return false, err
	}

	var cookies []*http.Cookie
	if err := json.Unmarshal(data, &cookies); err != nil {
		_ = c.cookieStore.Delete(ctx, c.cookieStoreKey())
		return false, err
	}

	u, err := url.Parse(c.cfg.BaseURL)
	if err != nil {
		return false, err
	}
	c.cookieJar.SetCookies(u, cookies)
	return c.hasSessionCookie(), nil
}

func (c *sqlParaClient) hasSessionCookie() bool {
	if c.cookieJar == nil {
		return false
	}
	u, err := url.Parse(c.cfg.BaseURL)
	if err != nil {
		return false
	}
	cookies := c.cookieJar.Cookies(u)
	logrus.Debugf("current cookie count: %d", len(cookies))
	for _, ck := range cookies {
		logrus.Debugf("  cookie: %s (domain=%s path=%s)", ck.Name, ck.Domain, ck.Path)
	}
	return len(cookies) > 0
}

func (c *sqlParaClient) ensureAuthenticated(ctx context.Context) error {
	c.mu.Lock()
	ok := c.authenticated && time.Now().Before(c.authExpiresAt)
	c.mu.Unlock()
	if ok {
		return nil
	}
	if !c.hasSessionCookie() {
		restored, err := c.restoreCookies(ctx)
		if err != nil {
			logrus.Warnf("restore sqlpara cookies from store failed: %v", err)
		}
		if restored {
			logrus.Debug("restored sqlpara cookies from store, skipping re-authentication")
			c.mu.Lock()
			c.authenticated = true
			c.authExpiresAt = time.Now().Add(c.cfg.AuthReuseInterval)
			c.mu.Unlock()
			return nil
		}
	}
	return c.authenticate(ctx)
}

func (c *sqlParaClient) invalidateAuth(ctx context.Context) {
	c.mu.Lock()
	c.authenticated = false
	c.mu.Unlock()
	if c.cookieJar != nil {
		if u, err := url.Parse(c.cfg.BaseURL); err == nil {
			c.cookieJar.SetCookies(u, nil)
		}
	}
	if c.cookieStore != nil {
		if err := c.cookieStore.Delete(ctx, c.cookieStoreKey()); err != nil {
			logrus.Warnf("delete sqlpara cookies from store failed: %v", err)
		}
	}
}

func isHTMLResponse(body []byte) bool {
	s := strings.TrimLeft(string(body), " \t\r\n\x00")
	p := strings.ToLower(s)
	return strings.HasPrefix(p, "<!doctype") || strings.HasPrefix(p, "<html")
}

// ------------------------------------------------------------------
// 数据库列表
// ------------------------------------------------------------------

func (c *sqlParaClient) ListDatabases(ctx context.Context, instanceName string) ([]string, error) {
	if err := c.ensureAuthenticated(ctx); err != nil {
		return nil, err
	}

	u := fmt.Sprintf("%s/instance/instance_resource/?instance_name=%s&resource_type=database",
		strings.TrimRight(c.cfg.BaseURL, "/"), url.QueryEscape(instanceName))
	return c.listStringResource(ctx, u)
}

func (c *sqlParaClient) listStringResource(ctx context.Context, u string) ([]string, error) {
	_, body, err := c.doGet(ctx, u)
	if err != nil {
		return nil, err
	}
	var apiResp apiResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("parse list: %w", err)
	}
	if apiResp.Status != 0 {
		return nil, fmt.Errorf("list status=%d msg=%s", apiResp.Status, apiResp.Msg)
	}
	var list []string
	if err := json.Unmarshal(apiResp.Data, &list); err != nil {
		return nil, fmt.Errorf("parse list data: %w", err)
	}
	return list, nil
}

// ------------------------------------------------------------------
// HTTP 工具
// ------------------------------------------------------------------

type authError struct {
	cause error
	body  string
}

func (a *authError) Error() string {
	return fmt.Sprintf("auth required: %v (body preview: %s)", a.cause, truncateString(a.body, 200))
}
func (a *authError) Unwrap() error { return a.cause }

func isAuthErrorType(err error) bool {
	if err == nil {
		return false
	}
	var ae *authError
	if _, ok := err.(*authError); ok {
		return true
	}
	if errors.As(err, &ae) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "unauthorized") ||
		strings.Contains(s, "access denied") ||
		strings.Contains(s, "login") ||
		strings.Contains(s, "401") ||
		strings.Contains(s, "forbidden") ||
		strings.Contains(s, "session expired") ||
		strings.Contains(s, "auth required")
}

func (c *sqlParaClient) doGet(ctx context.Context, u string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("build GET request: %w", err)
	}
	return c.do(req, u, false)
}

func (c *sqlParaClient) doGetAllowHTML(ctx context.Context, u string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("build GET request: %w", err)
	}
	return c.do(req, u, true)
}

func (c *sqlParaClient) doJsonPost(ctx context.Context, u string, body []byte) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("build POST request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, u, false)
}

func (c *sqlParaClient) doFormPost(ctx context.Context, u string, form url.Values) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, fmt.Errorf("build POST request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req, u, false)
}

func (c *sqlParaClient) doFormPostAllowHTML(ctx context.Context, u string, form url.Values) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, fmt.Errorf("build POST request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req, u, true)
}

func (c *sqlParaClient) do(req *http.Request, requestURL string, allowHTML bool) (*http.Response, []byte, error) {
	for k, v := range c.cfg.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", strings.TrimRight(c.cfg.BaseURL, "/"))
	req.Header.Set("Referer", strings.TrimRight(c.cfg.BaseURL, "/")+"/")
	req.Header.Set("User-Agent", "Mozilla/5.0 sql-proxy/1.0")
	if requestURL != "" {
		req.Header.Set("Referer", requestURL)
	}
	if csrfToken := c.getCsrfToken(); csrfToken != "" {
		req.Header.Set("X-CSRFToken", csrfToken)
	}

	logrus.Debugf("Request: %s %s %s", req.Method, requestURL, req.Header)

	httpResp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("send request: %w", err)
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return httpResp, nil, fmt.Errorf("read response: %w", err)
	}

	if !allowHTML && (isHTMLResponse(body) || httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden) {
		cause := fmt.Errorf("HTTP %d", httpResp.StatusCode)
		if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
			cause = fmt.Errorf("server returned login HTML page instead of JSON (HTTP %d)", httpResp.StatusCode)
		}
		return httpResp, body, &authError{cause: cause, body: string(body)}
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return httpResp, body, fmt.Errorf("HTTP %d: %s", httpResp.StatusCode, truncateString(string(body), 500))
	}

	return httpResp, body, nil
}

func (c *sqlParaClient) getCsrfToken() string {
	u, err := url.Parse(c.cfg.BaseURL)
	if err != nil {
		return ""
	}
	for _, ck := range c.cookieJar.Cookies(u) {
		if ck.Name == "csrftoken" {
			return ck.Value
		}
	}
	return ""
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
