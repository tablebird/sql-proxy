package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sql-proxy/internal/config"
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

type sqlParaClient struct {
	cfg        *config.BackendConfig
	httpClient *http.Client
	cookieJar  *cookiejar.Jar

	mu            sync.Mutex
	authenticated bool
	authExpiresAt time.Time
}

func (c *sqlParaClient) Execute(ctx context.Context, req *SQLRequest) (*SQLResponse, error) {
	db := req.Database
	instance := req.InstanceName

	var lastErr error
	var lastResp *SQLResponse
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
			logrus.Debugf("retry query, attempt %d/%d", attempt, c.cfg.MaxRetries)
		}

		resp, err := c.doSQLExecute(ctx, req.SQL, req.Args, instance, db)
		if err != nil {
			lastErr = err
			lastResp = resp
			if isAuthErrorType(err) {
				c.invalidateAuth()
				continue
			}
			logrus.Warnf("request failed (attempt %d): %v", attempt+1, err)
			continue
		}
		return resp, nil
	}

	if lastResp != nil {
		return lastResp, lastErr
	}
	return nil, fmt.Errorf("all retries failed: %w", lastErr)
}

func (c *sqlParaClient) Query(ctx context.Context, sql string, args ...interface{}) (*SQLResponse, error) {
	return c.Execute(ctx, &SQLRequest{
		SQL:  sql,
		Args: args,
	})
}

// ------------------------------------------------------------------
// 核心：SQL 执行（与接口 5 同结构）
// ------------------------------------------------------------------

func (c *sqlParaClient) doSQLExecute(ctx context.Context, sql string, args []interface{}, instanceName string, db string) (*SQLResponse, error) {
	if err := c.ensureAuthenticated(ctx); err != nil {
		return nil, err
	}

	if len(args) > 0 {
		sql = interpolateSQL(sql, args)
	}

	form := url.Values{}
	form.Set("instance_name", instanceName)
	form.Set("db_name", db)
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
		return &SQLResponse{Success: false, Error: apiResp.Msg},
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
		resp.Error = *qd.ErrorMsg
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

const authReuseInterval = 30 * time.Minute

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

	c.mu.Lock()
	c.authenticated = true
	c.authExpiresAt = time.Now().Add(authReuseInterval)
	c.mu.Unlock()
	logrus.Infof("authenticated to sqlpara as %s", c.cfg.Username)
	return nil
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
	return c.authenticate(ctx)
}

func (c *sqlParaClient) invalidateAuth() {
	c.mu.Lock()
	c.authenticated = false
	c.mu.Unlock()
	if c.cookieJar != nil {
		if u, err := url.Parse(c.cfg.BaseURL); err == nil {
			c.cookieJar.SetCookies(u, nil)
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

func interpolateSQL(sql string, args []interface{}) string {
	var b strings.Builder
	argIdx := 0
	runes := []rune(sql)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '?' && argIdx < len(args) {
			b.WriteString(formatArg(args[argIdx]))
			argIdx++
			continue
		}
		if r == '\'' || r == '"' {
			quote := r
			b.WriteRune(r)
			i++
			for i < len(runes) {
				c := runes[i]
				b.WriteRune(c)
				if c == '\\' && i+1 < len(runes) {
					b.WriteRune(runes[i+1])
					i += 2
					continue
				}
				if c == quote {
					break
				}
				i++
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func formatArg(v interface{}) string {
	if v == nil {
		return "NULL"
	}
	switch x := v.(type) {
	case string:
		return "'" + escapeString(x) + "'"
	case []byte:
		return "'" + escapeString(string(x)) + "'"
	case bool:
		if x {
			return "1"
		}
		return "0"
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return fmt.Sprintf("%v", x)
	default:
		return "'" + escapeString(fmt.Sprintf("%v", x)) + "'"
	}
}

func escapeString(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\'':
			b.WriteString("''")
		case '\\':
			b.WriteString("\\\\")
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\t':
			b.WriteString("\\t")
		case '\x00':
			b.WriteString("\\0")
		case '\x1a':
			b.WriteString("\\Z")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
