package backend

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"

	"sql-proxy/internal/config"
	"sql-proxy/internal/sql"
	"sql-proxy/internal/store"

	"github.com/sirupsen/logrus"
)

// ------------------------------------------------------------------
// 对外兼容接口：Request / Response（保持 handler.go 无需修改）
// ------------------------------------------------------------------

type SQLRequest struct {
	SQL          string         `json:"sql"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	Args         []interface{}  `json:"args,omitempty"`
	InstanceName string         `json:"instance_name,omitempty"`
	Database     string         `json:"database,omitempty"`
	MaxRows      int            `json:"max_rows,omitempty"`
	TimeoutMs    int            `json:"timeout_ms,omitempty"`
}

func (r *SQLRequest) InterpolateSQL() string {
	if len(r.Args) > 0 {
		return sql.InterpolateSQL(r.SQL, r.Args)
	}
	return r.SQL
}

type SQLResponse struct {
	Success      bool           `json:"success"`
	Message      string         `json:"message,omitempty"`
	Results      []*QueryResult `json:"results,omitempty"`
	RowCount     int64          `json:"row_count,omitempty"`
	LastInsertID int64          `json:"last_insert_id,omitempty"`
}

func (r *SQLResponse) Error() string {
	if r.Success {
		return ""
	}
	return r.Message
}

type QueryResult struct {
	Columns []string        `json:"columns"`
	Types   []string        `json:"types"`
	Rows    [][]interface{} `json:"rows"`
}

// ------------------------------------------------------------------
// 元数据接口的结构
// ------------------------------------------------------------------

type DBInstance struct {
	ID           int    `json:"id"`
	Type         string `json:"type"`
	DBType       string `json:"db_type"`
	InstanceName string `json:"instance_name"`
}

// ------------------------------------------------------------------
// Client
// ------------------------------------------------------------------

type Client interface {
	Execute(ctx context.Context, req *SQLRequest) (*SQLResponse, error)

	PreSubmit(ctx context.Context, req *SQLRequest) error

	Submit(ctx context.Context, req *SQLRequest) (*SQLResponse, error)

	ListDatabases(ctx context.Context, instanceName string) ([]string, error)
}

func NewClient(cfg *config.BackendConfig) Client {
	jar, _ := cookiejar.New(nil)

	transport := &http.Transport{}
	if cfg.ProxyUrl != "" {
		proxyUrl, err := url.Parse(cfg.ProxyUrl)
		if err != nil {
			logrus.WithError(err).Error("Failed to parse proxy URL")
		} else {
			transport.Proxy = http.ProxyURL(proxyUrl)
		}
	}
	return &sqlParaClient{
		cfg: cfg,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   time.Duration(cfg.Timeout) * time.Second,
			Jar:       jar,
		},
		cookieJar:   jar,
		cookieStore: store.NewRedisCookieStore(),
	}
}
