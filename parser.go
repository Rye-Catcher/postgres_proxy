package main

import (
	"strings"
)

// QueryType represents whether a SQL query is a read or a write.
type QueryType int

const (
	// QueryTypeRead covers SELECT and other read-only statements.
	QueryTypeRead QueryType = iota
	// QueryTypeWrite covers INSERT, UPDATE, DELETE, and DDL statements.
	QueryTypeWrite
)

func (qt QueryType) String() string {
	switch qt {
	case QueryTypeRead:
		return "read"
	case QueryTypeWrite:
		return "write"
	default:
		return "unknown"
	}
}

// ParseQueryType inspects the first meaningful keyword in sql and returns the
// corresponding QueryType.  Unrecognised statements are treated as writes so
// that they are always sent to the primary.
func ParseQueryType(sql string) QueryType {
	keyword := firstKeyword(sql)
	if keyword == "SELECT" || keyword == "SHOW" || keyword == "EXPLAIN" || keyword == "DESCRIBE" || keyword == "WITH" {
		return QueryTypeRead
	}
	return QueryTypeWrite
}

// firstKeyword returns the upper-cased first non-whitespace word in sql.
func firstKeyword(sql string) string {
	sql = strings.TrimSpace(sql)
	// Skip leading comments (-- line comments and /* block comments */).
	for {
		if strings.HasPrefix(sql, "--") {
			idx := strings.IndexByte(sql, '\n')
			if idx == -1 {
				return ""
			}
			sql = strings.TrimSpace(sql[idx+1:])
			continue
		}
		if strings.HasPrefix(sql, "/*") {
			idx := strings.Index(sql, "*/")
			if idx == -1 {
				return ""
			}
			sql = strings.TrimSpace(sql[idx+2:])
			continue
		}
		break
	}

	idx := strings.IndexAny(sql, " \t\n\r(;")
	if idx == -1 {
		return strings.ToUpper(sql)
	}
	return strings.ToUpper(sql[:idx])
}
