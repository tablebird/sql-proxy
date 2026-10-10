package sql

import (
	"encoding/json"
	"strings"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/format"
	_ "github.com/pingcap/tidb/pkg/parser/test_driver"
)

// ProxyResult 预处理后的代理上下文
type ParseResult struct {
	Metadata map[string]any
	CleanSQL string
}

func ParseClientSQL(rawSQL string) (*ParseResult, error) {
	cleanSQL, err := stripLeadingComments(rawSQL)
	if err != nil {
		return nil, err
	}
	var meta map[string]any
	// 1. 提取头部第一个多行注释作为元数据 (匹配 /* ... */)
	trimmedSQL := strings.TrimSpace(rawSQL)
	if strings.HasPrefix(trimmedSQL, "/*") {
		endIdx := strings.Index(trimmedSQL, "*/")
		if endIdx != -1 {
			commentContent := strings.TrimSpace(trimmedSQL[2:endIdx])
			// 尝试解析 JSON 格式的元数据
			_ = json.Unmarshal([]byte(commentContent), &meta)
		}
	}
	return &ParseResult{
		Metadata: meta,
		CleanSQL: cleanSQL,
	}, nil
}

func stripLeadingComments(sql string) (string, error) {
	p := parser.New()

	stmtNotes, _, err := p.Parse(sql, "", "")
	if err != nil {
		return "", err
	}
	var all_buf strings.Builder
	lineCount := len(stmtNotes)
	for _, stmt := range stmtNotes {
		var buf strings.Builder
		flags := format.RestoreStringSingleQuotes |
			format.RestoreKeyWordUppercase |
			format.RestoreStringWithoutCharset |
			format.RestoreStringWithoutDefaultCharset
		ctx := format.NewRestoreCtx(flags, &buf)
		err = stmt.Restore(ctx)
		if err != nil {
			return "", err
		}
		all_buf.WriteString(buf.String())
		if lineCount > 1 {
			all_buf.WriteString(";\n")
		}
	}

	return all_buf.String(), nil
}
