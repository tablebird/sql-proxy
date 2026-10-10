package sql

import (
	"fmt"
	"strings"
)

func InterpolateSQL(sql string, args []interface{}) string {
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
