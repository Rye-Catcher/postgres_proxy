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
//
// Routing policy summary:
//   - Multi-statement queries (separated by ";") → primary (safe default)
//   - SELECT … FOR UPDATE/SHARE/NO KEY UPDATE/KEY SHARE → primary (row locks)
//   - SELECT calling nextval/setval/pg_advisory_lock/pg_advisory_unlock/txid_current → primary (side effects)
//   - WITH CTE containing INSERT/UPDATE/DELETE → primary
//   - EXPLAIN ANALYZE → primary (it executes the query)
//   - Plain SELECT, SHOW, EXPLAIN (without ANALYZE), DESCRIBE, WITH (read-only CTE) → replica
//   - Everything else → primary
func ParseQueryType(sql string) QueryType {
	// Multi-statement queries are always routed to primary (safest policy).
	if isMultiStatement(sql) {
		return QueryTypeWrite
	}

	keyword := firstKeyword(sql)
	switch keyword {
	case "SELECT":
		if selectMustBePrimary(sql) {
			return QueryTypeWrite
		}
		return QueryTypeRead
	case "WITH":
		if withContainsWrite(sql) {
			return QueryTypeWrite
		}
		return QueryTypeRead
	case "EXPLAIN":
		if explainMustBePrimary(sql) {
			return QueryTypeWrite
		}
		return QueryTypeRead
	case "SHOW", "DESCRIBE":
		return QueryTypeRead
	default:
		return QueryTypeWrite
	}
}

// isMultiStatement returns true when sql contains more than one statement
// (i.e. a semicolon is followed by non-whitespace content outside of
// comments).
func isMultiStatement(sql string) bool {
	stripped := stripComments(sql)
	idx := strings.Index(stripped, ";")
	if idx == -1 {
		return false
	}
	rest := strings.TrimSpace(stripped[idx+1:])
	return len(rest) > 0
}

// selectMustBePrimary returns true for SELECT queries that must be executed on
// the primary because they acquire row-level locks or call state-modifying
// functions.
func selectMustBePrimary(sql string) bool {
	upper := strings.ToUpper(sql)
	// Row-level locking clauses.
	for _, clause := range []string{" FOR UPDATE", " FOR SHARE", " FOR NO KEY UPDATE", " FOR KEY SHARE"} {
		if strings.Contains(upper, clause) {
			return true
		}
	}
	// State-modifying / session-sensitive functions.
	for _, fn := range []string{"NEXTVAL(", "SETVAL(", "PG_ADVISORY_LOCK(", "PG_ADVISORY_UNLOCK(", "TXID_CURRENT("} {
		if strings.Contains(upper, fn) {
			return true
		}
	}
	return false
}

// withContainsWrite returns true when a CTE (WITH …) contains at least one
// INSERT, UPDATE, or DELETE clause, making it a write operation.
func withContainsWrite(sql string) bool {
	upper := strings.ToUpper(stripComments(sql))
	for _, kw := range []string{"INSERT", "UPDATE", "DELETE"} {
		if containsWord(upper, kw) {
			return true
		}
	}
	return false
}

// explainMustBePrimary returns true when EXPLAIN is used with the ANALYZE
// option, which actually executes the statement.
func explainMustBePrimary(sql string) bool {
	upper := strings.ToUpper(strings.TrimSpace(sql))
	// Consume the EXPLAIN keyword.
	rest := strings.TrimSpace(upper[len("EXPLAIN"):])
	// EXPLAIN ANALYZE ...  or  EXPLAIN (ANALYZE ...) ...
	if strings.HasPrefix(rest, "ANALYZE") {
		return true
	}
	if strings.HasPrefix(rest, "(") && strings.Contains(rest, "ANALYZE") {
		return true
	}
	return false
}

// containsWord returns true if kw appears in sql as a standalone word
// (not adjacent to a letter, digit, or underscore).
func containsWord(sql, kw string) bool {
	for i := 0; i <= len(sql)-len(kw); i++ {
		if sql[i:i+len(kw)] != kw {
			continue
		}
		before := i == 0 || !isWordChar(sql[i-1])
		after := i+len(kw) == len(sql) || !isWordChar(sql[i+len(kw)])
		if before && after {
			return true
		}
	}
	return false
}

// isWordChar returns true for characters that can appear in a SQL identifier.
func isWordChar(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_'
}

// stripComments removes SQL line comments (--) and block comments (/* */).
func stripComments(sql string) string {
	var b strings.Builder
	for len(sql) > 0 {
		if strings.HasPrefix(sql, "--") {
			idx := strings.IndexByte(sql, '\n')
			if idx == -1 {
				return b.String()
			}
			b.WriteByte('\n')
			sql = sql[idx+1:]
		} else if strings.HasPrefix(sql, "/*") {
			idx := strings.Index(sql, "*/")
			if idx == -1 {
				return b.String()
			}
			b.WriteByte(' ')
			sql = sql[idx+2:]
		} else {
			b.WriteByte(sql[0])
			sql = sql[1:]
		}
	}
	return b.String()
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
