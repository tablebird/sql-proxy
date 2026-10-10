package proxy

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"sql-proxy/internal/backend"
	"sql-proxy/internal/sql"

	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/server"

	"github.com/sirupsen/logrus"
)

const (
	DefaultCharset   uint16 = 33
	ERUnknownError   uint16 = 1105
	ERNotImplemented uint16 = 1235
)

type Handler struct {
	backend      backend.Client
	connID       uint32
	instanceName string
	currentDB    string
	conn         *server.Conn // 存储连接对象以获取连接属性
}

func NewHandler(b backend.Client, connID uint32) *Handler {
	return &Handler{
		backend: b,
		connID:  connID,
	}
}

func (h *Handler) UseDB(dbName string) error {
	logrus.Infof("[conn %d] USE database: %s", h.connID, dbName)
	if strings.Contains(dbName, "/") {
		dbs := strings.SplitN(dbName, "/", 2)
		h.instanceName = dbs[0]
		h.currentDB = dbs[1]
	} else {
		h.currentDB = dbName
	}
	return nil
}

func (h *Handler) setAttributes(attrs map[string]string) error {
	instanceName := attrs["instance_name"]

	if instanceName != "" {
		h.instanceName = instanceName
	}

	return nil
}

func (h *Handler) HandleQuery(query string) (*mysql.Result, error) {
	logrus.Infof("[conn %d] Query: %s", h.connID, truncateQuery(query))
	ctx := context.Background()

	result, err := sql.ParseClientSQL(query)
	if err != nil {
		return nil, mysql.NewError(ERUnknownError, err.Error())
	}

	qType := classifyQuery(result.CleanSQL)

	switch qType {
	case QueryTypeShow:
		return h.handleShowQuery(ctx, result.CleanSQL)
	case QueryTypeUse:
		h.UseDB(strings.TrimPrefix(result.CleanSQL, "USE "))
		return h.buildEmptyOK()
	case QueryTypeSet:
		return h.buildEmptyOK()
	case QueryTypeBegin, QueryTypeCommit, QueryTypeRollback:
		return h.buildEmptyOK()
	case QueryTypeSelect:
		return h.handleSelectQuery(ctx, result.CleanSQL)
	case QueryTypeUpdate, QueryTypeInsert, QueryTypeDelete:
		return h.handleSubmit(ctx, result)
	default:
		return h.buildEmptyOK()
	}
}

func (h *Handler) handleSubmit(ctx context.Context, result *sql.ParseResult) (*mysql.Result, error) {
	err := h.backend.PreSubmit(ctx, &backend.SQLRequest{
		SQL:      result.CleanSQL,
		Metadata: result.Metadata,
	})
	if err != nil {
		return nil, mysql.NewError(ERUnknownError, err.Error())
	}
	resp, err := h.backend.Submit(ctx, &backend.SQLRequest{
		SQL:      result.CleanSQL,
		Metadata: result.Metadata,
	})
	if err != nil {
		return nil, mysql.NewError(ERUnknownError, err.Error())
	}
	return &mysql.Result{
		StatusMessage: resp.Message,
	}, nil
}

func (h *Handler) handleGenericQuery(ctx context.Context, query string) (*mysql.Result, error) {
	resp, err := h.backend.Execute(ctx, &backend.SQLRequest{
		SQL:          query,
		Database:     h.currentDB,
		InstanceName: h.instanceName,
	})
	if err != nil {
		if resp != nil && resp.Error() != "" {
			return nil, mysql.NewError(ERUnknownError, resp.Error())
		}
		return nil, mysql.NewError(ERUnknownError, err.Error())
	}

	return h.convertToMySQLResult(resp)
}

func (h *Handler) convertToMySQLResult(resp *backend.SQLResponse) (*mysql.Result, error) {
	result := &mysql.Result{
		AffectedRows: uint64(resp.RowCount),
		InsertId:     uint64(resp.LastInsertID),
	}

	if len(resp.Results) == 0 {
		return result, nil
	}

	firstResult := resp.Results[0]
	if len(firstResult.Columns) == 0 {
		return result, nil
	}

	resultSet, err := mysql.BuildSimpleTextResultset(firstResult.Columns, firstResult.Rows)
	if err != nil {
		return nil, fmt.Errorf("build result set: %w", err)
	}

	for i, col := range firstResult.Columns {
		if resultSet.Fields[i] == nil {
			continue
		}
		resultSet.Fields[i].Name = []byte(col)
		resultSet.Fields[i].OrgName = []byte(col)
		resultSet.Fields[i].Charset = DefaultCharset
		if i < len(firstResult.Types) {
			resultSet.Fields[i].Type = mapMySQLType(firstResult.Types[i])
		}
	}

	result.Resultset = resultSet
	return result, nil
}

func (h *Handler) handleShowQuery(ctx context.Context, query string) (*mysql.Result, error) {
	queryLower := strings.ToLower(query)

	switch {
	case regexp.MustCompile(`(?i)^show\s+databases`).MatchString(query):
		return h.buildShowDatabasesResult(ctx)
	case regexp.MustCompile(`(?i)^show\s+tables`).MatchString(query):
		return h.handleGenericQuery(ctx, query)
	case regexp.MustCompile(`(?i)^show\s+columns\s+from|^describe\s+|^desc\s+`).MatchString(query):
		return h.handleGenericQuery(ctx, query)
	case regexp.MustCompile(`(?i)^show\s+variables`).MatchString(query):
		return h.buildShowVariablesResult()
	case regexp.MustCompile(`(?i)^show\s+status`).MatchString(query):
		return h.buildShowStatusResult()
	case regexp.MustCompile(`(?i)^show\s+create\s+view\s+` + "`?INFORMATION_SCHEMA`?\\..+").MatchString(query):
		return h.buildCreateViewResult()
	case strings.HasPrefix(queryLower, "select version()") ||
		strings.Contains(queryLower, "@@version_comment"):
		return h.buildVersionResult()
	default:
		return h.handleGenericQuery(ctx, query)
	}
}

func (h *Handler) handleSelectQuery(ctx context.Context, query string) (*mysql.Result, error) {
	switch {
	case regexp.MustCompile(`(?i)select.+from\s+mysql\.user\s+.+`).MatchString(query):
		return h.buildEmptyOK()
	case regexp.MustCompile(`(?i)^select\s+(@@SESSION.)?tx_read_only`).MatchString(query):
		return h.buildTxReadOnlyResult()
	default:
		return h.handleGenericQuery(ctx, query)
	}
}

func (h *Handler) buildShowDatabasesResult(ctx context.Context) (*mysql.Result, error) {
	database, err := h.backend.ListDatabases(ctx, h.instanceName)
	if err != nil {
		logrus.Errorf("[conn %d] failed to list databases: %v", h.connID, err)
	}
	names := []string{"Database"}
	var rows [][]any
	for _, db := range database {
		rows = append(rows, []any{db})
	}
	return buildTextResult(names, rows)
}

func (h *Handler) buildShowVariablesResult() (*mysql.Result, error) {
	names := []string{"Variable_name", "Value"}
	rows := [][]any{
		{"version", "8.0.33-sql-proxy"},
		{"character_set_client", "utf8mb4"},
		{"character_set_connection", "utf8mb4"},
		{"character_set_results", "utf8mb4"},
		{"character_set_server", "utf8mb4"},
		{"max_allowed_packet", "67108864"},
		{"sql_mode", ""},
		{"lower_case_table_names", "0"},
	}
	return buildTextResult(names, rows)
}

func (h *Handler) buildShowStatusResult() (*mysql.Result, error) {
	names := []string{"Variable_name", "Value"}
	rows := [][]any{
		{"Uptime", "0"},
		{"Threads_connected", "1"},
		{"Queries", "0"},
	}
	return buildTextResult(names, rows)
}

func (h *Handler) buildCreateViewResult() (*mysql.Result, error) {
	names := []string{"View", "Create View"}
	rows := [][]any{}
	return buildTextResult(names, rows)
}

func (h *Handler) buildVersionResult() (*mysql.Result, error) {
	names := []string{"version()"}
	rows := [][]any{
		{"8.0.33-sql-proxy"},
	}
	return buildTextResult(names, rows)
}

func (h *Handler) buildTxReadOnlyResult() (*mysql.Result, error) {
	names := []string{"tx_read_only"}
	rows := [][]any{
		{"0"},
	}
	return buildTextResult(names, rows)
}

func (h *Handler) buildEmptyOK() (*mysql.Result, error) {
	return &mysql.Result{}, nil
}

func buildTextResult(names []string, rows [][]any) (*mysql.Result, error) {
	resultSet, err := mysql.BuildSimpleTextResultset(names, rows)
	if err != nil {
		return nil, err
	}
	for _, f := range resultSet.Fields {
		if f != nil {
			f.Charset = DefaultCharset
		}
	}
	return &mysql.Result{
		Resultset: resultSet,
	}, nil
}

func (h *Handler) HandleFieldList(table string, fieldWildcard string) ([]*mysql.Field, error) {
	logrus.Infof("[conn %d] FieldList: table=%s, wildcard=%s", h.connID, table, fieldWildcard)
	return nil, mysql.NewError(ERNotImplemented, "field list not implemented")
}

func (h *Handler) HandleStmtPrepare(query string) (int, int, interface{}, error) {
	logrus.Infof("[conn %d] StmtPrepare: %s", h.connID, truncateQuery(query))
	ctx := context.Background()

	paramsCount := countPlaceholders(query)
	resp, err := h.backend.Execute(ctx, &backend.SQLRequest{
		SQL:          query,
		Database:     h.currentDB,
		InstanceName: h.instanceName,
	})
	if err != nil && resp == nil {
		return 0, 0, nil, mysql.NewError(ERUnknownError, err.Error())
	}

	columnsCount := 0
	if resp != nil && len(resp.Results) > 0 {
		columnsCount = len(resp.Results[0].Columns)
	}

	return paramsCount, columnsCount, query, nil
}

func (h *Handler) HandleStmtExecute(stmtContext interface{}, query string, args []interface{}) (*mysql.Result, error) {
	logrus.Infof("[conn %d] StmtExecute: %s, args count=%d", h.connID, truncateQuery(query), len(args))
	ctx := context.Background()
	_ = stmtContext

	resp, err := h.backend.Execute(ctx, &backend.SQLRequest{
		SQL:          query,
		Args:         args,
		Database:     h.currentDB,
		InstanceName: h.instanceName,
	})
	if err != nil {
		if resp != nil && resp.Error() != "" {
			return nil, mysql.NewError(ERUnknownError, resp.Error())
		}
		return nil, mysql.NewError(ERUnknownError, err.Error())
	}

	return h.convertToMySQLResult(resp)
}

func (h *Handler) HandleStmtClose(stmtContext interface{}) error {
	_ = stmtContext
	return nil
}

func (h *Handler) HandleOtherCommand(cmd byte, data []byte) error {
	logrus.Debugf("[conn %d] OtherCommand: cmd=%d", h.connID, cmd)
	return mysql.NewError(ERNotImplemented,
		fmt.Sprintf("command %d is not supported", cmd))
}

type QueryType int

const (
	QueryTypeSelect QueryType = iota
	QueryTypeInsert
	QueryTypeUpdate
	QueryTypeDelete
	QueryTypeShow
	QueryTypeSet
	QueryTypeBegin
	QueryTypeCommit
	QueryTypeRollback
	QueryTypeUse
	QueryTypeOther
)

func classifyQuery(query string) QueryType {
	q := strings.ToLower(strings.TrimSpace(query))

	switch {
	case strings.HasPrefix(q, "select"):
		return QueryTypeSelect
	case strings.HasPrefix(q, "use"):
		return QueryTypeUse
	case strings.HasPrefix(q, "insert"):
		return QueryTypeInsert
	case strings.HasPrefix(q, "update"):
		return QueryTypeUpdate
	case strings.HasPrefix(q, "delete"):
		return QueryTypeDelete
	case strings.HasPrefix(q, "show"), strings.HasPrefix(q, "describe"), strings.HasPrefix(q, "desc"):
		return QueryTypeShow
	case strings.HasPrefix(q, "set"):
		return QueryTypeSet
	case strings.HasPrefix(q, "begin"), strings.HasPrefix(q, "start transaction"):
		return QueryTypeBegin
	case strings.HasPrefix(q, "commit"):
		return QueryTypeCommit
	case strings.HasPrefix(q, "rollback"):
		return QueryTypeRollback
	default:
		return QueryTypeOther
	}
}

func countPlaceholders(query string) int {
	count := 0
	for _, c := range query {
		if c == '?' {
			count++
		}
	}
	return count
}

func truncateQuery(q string) string {
	if len(q) <= 500 {
		return q
	}
	return q[:500] + "..."
}

func mapMySQLType(typeName string) uint8 {
	t := strings.ToLower(typeName)
	switch t {
	case "int", "integer", "bigint", "smallint", "tinyint", "mediumint":
		return mysql.MYSQL_TYPE_LONGLONG
	case "float", "double", "real":
		return mysql.MYSQL_TYPE_DOUBLE
	case "decimal", "numeric":
		return mysql.MYSQL_TYPE_NEWDECIMAL
	case "bool", "boolean":
		return mysql.MYSQL_TYPE_TINY
	case "date":
		return mysql.MYSQL_TYPE_DATE
	case "datetime", "timestamp":
		return mysql.MYSQL_TYPE_DATETIME
	case "time":
		return mysql.MYSQL_TYPE_TIME
	case "year":
		return mysql.MYSQL_TYPE_YEAR
	case "json":
		return mysql.MYSQL_TYPE_JSON
	case "blob", "longblob", "mediumblob", "tinyblob", "binary", "varbinary":
		return mysql.MYSQL_TYPE_BLOB
	default:
		return mysql.MYSQL_TYPE_VAR_STRING
	}
}

var _ server.Handler = (*Handler)(nil)
