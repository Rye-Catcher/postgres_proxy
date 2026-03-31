package main

import (
	"testing"
)

// ---------------------------------------------------------------------------
// Basic routing
// ---------------------------------------------------------------------------

func TestParseQueryType_Reads(t *testing.T) {
	reads := []string{
		"SELECT * FROM users",
		"select id from orders where id = 1",
		"  SELECT 1",
		"SHOW search_path",
		"EXPLAIN SELECT * FROM users",
		"DESCRIBE users",
		"WITH cte AS (SELECT 1) SELECT * FROM cte",
		// With leading comment
		"-- get all users\nSELECT * FROM users",
		"/* admin */ SELECT 1",
	}

	for _, sql := range reads {
		t.Run(sql, func(t *testing.T) {
			got := ParseQueryType(sql)
			if got != QueryTypeRead {
				t.Errorf("ParseQueryType(%q) = %v, want Read", sql, got)
			}
		})
	}
}

func TestParseQueryType_Writes(t *testing.T) {
	writes := []string{
		"INSERT INTO users (name) VALUES ('alice')",
		"UPDATE users SET name='bob' WHERE id=1",
		"DELETE FROM users WHERE id=1",
		"CREATE TABLE t (id INT)",
		"DROP TABLE t",
		"ALTER TABLE t ADD COLUMN x INT",
		"TRUNCATE TABLE t",
		"BEGIN",
		"COMMIT",
		"ROLLBACK",
		"GRANT SELECT ON t TO user1",
		"REVOKE SELECT ON t FROM user1",
	}

	for _, sql := range writes {
		t.Run(sql, func(t *testing.T) {
			got := ParseQueryType(sql)
			if got != QueryTypeWrite {
				t.Errorf("ParseQueryType(%q) = %v, want Write", sql, got)
			}
		})
	}
}

func TestParseQueryType_Empty(t *testing.T) {
	// Empty / comment-only queries default to write (safe default: send to primary).
	got := ParseQueryType("")
	if got != QueryTypeWrite {
		t.Errorf("ParseQueryType(\"\") = %v, want Write", got)
	}

	got = ParseQueryType("-- just a comment")
	if got != QueryTypeWrite {
		t.Errorf("ParseQueryType(comment only) = %v, want Write", got)
	}
}

// ---------------------------------------------------------------------------
// SELECTs that must still go to primary
// ---------------------------------------------------------------------------

func TestParseQueryType_SelectToPrimary(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		{"nextval", "SELECT nextval('seq_test')"},
		{"setval", "SELECT setval('seq_test', 100)"},
		{"pg_advisory_lock", "SELECT pg_advisory_lock(1)"},
		{"pg_advisory_unlock", "SELECT pg_advisory_unlock(1)"},
		{"for_update", "SELECT * FROM users FOR UPDATE"},
		{"for_share", "SELECT * FROM users FOR SHARE"},
		{"for_no_key_update", "SELECT * FROM users FOR NO KEY UPDATE"},
		{"for_key_share", "SELECT * FROM users FOR KEY SHARE"},
		{"txid_current", "SELECT txid_current()"},
		// Mixed case
		{"nextval_mixed", "select nextval('seq_test')"},
		{"for_update_mixed", "Select * From users For Update"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseQueryType(tc.sql)
			if got != QueryTypeWrite {
				t.Errorf("ParseQueryType(%q) = %v, want Write (primary)", tc.sql, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CTE traps
// ---------------------------------------------------------------------------

func TestParseQueryType_CTETraps(t *testing.T) {
	reads := []struct {
		name string
		sql  string
	}{
		{"read_only_cte", "WITH x AS (SELECT * FROM users) SELECT * FROM x"},
		{"nested_read_cte", "WITH a AS (SELECT 1), b AS (SELECT 2) SELECT * FROM a, b"},
	}

	for _, tc := range reads {
		t.Run("read_"+tc.name, func(t *testing.T) {
			got := ParseQueryType(tc.sql)
			if got != QueryTypeRead {
				t.Errorf("ParseQueryType(%q) = %v, want Read", tc.sql, got)
			}
		})
	}

	writes := []struct {
		name string
		sql  string
	}{
		{"insert_cte", "WITH ins AS (INSERT INTO users(name) VALUES ('a') RETURNING id) SELECT * FROM ins"},
		{"update_cte", "WITH upd AS (UPDATE users SET name='c' WHERE id=1 RETURNING *) SELECT * FROM upd"},
		{"delete_cte", "WITH del AS (DELETE FROM users WHERE id=1 RETURNING *) SELECT * FROM del"},
	}

	for _, tc := range writes {
		t.Run("write_"+tc.name, func(t *testing.T) {
			got := ParseQueryType(tc.sql)
			if got != QueryTypeWrite {
				t.Errorf("ParseQueryType(%q) = %v, want Write (primary)", tc.sql, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Comments and whitespace
// ---------------------------------------------------------------------------

func TestParseQueryType_CommentsAndWhitespace(t *testing.T) {
	reads := []struct {
		name string
		sql  string
	}{
		{"plain_select", "SELECT * FROM users"},
		{"line_comment_before_select", "-- comment\nSELECT * FROM users"},
		{"block_comment_before_select", "/* comment */ SELECT * FROM users"},
		{"misleading_block_comment", "/* INSERT INTO fake */ SELECT * FROM users"},
		{"leading_whitespace", "   \t  SELECT * FROM users"},
	}

	for _, tc := range reads {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseQueryType(tc.sql)
			if got != QueryTypeRead {
				t.Errorf("ParseQueryType(%q) = %v, want Read", tc.sql, got)
			}
		})
	}

	writes := []struct {
		name string
		sql  string
	}{
		{"line_comment_before_insert", "-- SELECT\nINSERT INTO users VALUES (1,'a')"},
		{"block_comment_before_insert", "/* SELECT * FROM x */ INSERT INTO users VALUES (1,'a')"},
	}

	for _, tc := range writes {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseQueryType(tc.sql)
			if got != QueryTypeWrite {
				t.Errorf("ParseQueryType(%q) = %v, want Write", tc.sql, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Case sensitivity
// ---------------------------------------------------------------------------

func TestParseQueryType_CaseSensitivity(t *testing.T) {
	cases := []struct {
		sql  string
		want QueryType
	}{
		{"select * from users", QueryTypeRead},
		{"SeLeCt * FrOm users", QueryTypeRead},
		{"SELECT * FROM users", QueryTypeRead},
		{"InSeRt into users values (1,'a')", QueryTypeWrite},
		{"insert into users values (1,'a')", QueryTypeWrite},
		{"INSERT INTO users VALUES (1,'a')", QueryTypeWrite},
		{"uPdAtE users SET name='b' WHERE id=1", QueryTypeWrite},
		{"dElEtE FROM users WHERE id=1", QueryTypeWrite},
	}

	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			got := ParseQueryType(tc.sql)
			if got != tc.want {
				t.Errorf("ParseQueryType(%q) = %v, want %v", tc.sql, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Multi-statement queries
// ---------------------------------------------------------------------------

func TestParseQueryType_MultiStatement(t *testing.T) {
	// All multi-statement queries must route to primary.
	writes := []struct {
		name string
		sql  string
	}{
		{"select_then_insert", "SELECT * FROM users; INSERT INTO users VALUES (2,'b')"},
		{"begin_select_commit", "BEGIN; SELECT * FROM users; COMMIT"},
		{"set_then_select", "SET search_path TO public; SELECT * FROM users"},
		{"two_selects", "SELECT 1; SELECT 2"},
	}

	for _, tc := range writes {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseQueryType(tc.sql)
			if got != QueryTypeWrite {
				t.Errorf("ParseQueryType(%q) = %v, want Write (primary)", tc.sql, got)
			}
		})
	}

	// A trailing semicolon with no following content is NOT multi-statement.
	singleWithTrailingSemicolon := "SELECT * FROM users;"
	got := ParseQueryType(singleWithTrailingSemicolon)
	if got != QueryTypeRead {
		t.Errorf("ParseQueryType(%q) = %v, want Read (trailing semicolon is OK)", singleWithTrailingSemicolon, got)
	}
}

// ---------------------------------------------------------------------------
// EXPLAIN variants
// ---------------------------------------------------------------------------

func TestParseQueryType_Explain(t *testing.T) {
	reads := []struct {
		name string
		sql  string
	}{
		{"explain_select", "EXPLAIN SELECT * FROM users"},
		{"explain_verbose_select", "EXPLAIN (VERBOSE) SELECT * FROM users"},
		{"explain_format_json", "EXPLAIN (FORMAT JSON) SELECT * FROM users"},
	}

	for _, tc := range reads {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseQueryType(tc.sql)
			if got != QueryTypeRead {
				t.Errorf("ParseQueryType(%q) = %v, want Read", tc.sql, got)
			}
		})
	}

	writes := []struct {
		name string
		sql  string
	}{
		{"explain_analyze_select", "EXPLAIN ANALYZE SELECT * FROM users"},
		{"explain_analyze_insert", "EXPLAIN ANALYZE INSERT INTO users(name) VALUES ('a')"},
		{"explain_analyze_update", "EXPLAIN ANALYZE UPDATE users SET name='b' WHERE id=1"},
		{"explain_paren_analyze", "EXPLAIN (ANALYZE) SELECT * FROM users"},
		{"explain_analyze_buffers", "EXPLAIN (ANALYZE, BUFFERS) SELECT * FROM users"},
	}

	for _, tc := range writes {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseQueryType(tc.sql)
			if got != QueryTypeWrite {
				t.Errorf("ParseQueryType(%q) = %v, want Write (primary)", tc.sql, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// COPY and special statements
// ---------------------------------------------------------------------------

func TestParseQueryType_SpecialStatements(t *testing.T) {
	// All maintenance / session / special statements → primary.
	writes := []struct {
		name string
		sql  string
	}{
		{"copy_from", "COPY users FROM STDIN"},
		{"copy_to", "COPY (SELECT * FROM users) TO STDOUT"},
		{"vacuum", "VACUUM"},
		{"analyze", "ANALYZE"},
		{"reindex", "REINDEX TABLE users"},
		{"listen", "LISTEN channel1"},
		{"notify", "NOTIFY channel1, 'msg'"},
		{"set_app_name", "SET application_name='proxy-test'"},
		{"reset_all", "RESET ALL"},
		{"discard_all", "DISCARD ALL"},
		{"prepare_select", "PREPARE s AS SELECT * FROM users WHERE id=$1"},
		{"prepare_insert", "PREPARE ins AS INSERT INTO users(name) VALUES ($1)"},
		{"execute", "EXECUTE s(1)"},
		{"call", "CALL my_procedure()"},
		{"do_block", "DO $$ BEGIN NULL; END $$"},
	}

	for _, tc := range writes {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseQueryType(tc.sql)
			if got != QueryTypeWrite {
				t.Errorf("ParseQueryType(%q) = %v, want Write (primary)", tc.sql, got)
			}
		})
	}

	// SHOW is read-only.
	reads := []struct {
		name string
		sql  string
	}{
		{"show_search_path", "SHOW search_path"},
		{"show_all", "SHOW ALL"},
	}

	for _, tc := range reads {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseQueryType(tc.sql)
			if got != QueryTypeRead {
				t.Errorf("ParseQueryType(%q) = %v, want Read", tc.sql, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// firstKeyword and helpers
// ---------------------------------------------------------------------------

func TestFirstKeyword(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"SELECT 1", "SELECT"},
		{"  insert into t", "INSERT"},
		{"(SELECT 1)", ""},  // leading '(' stops at first non-alpha
		{"-- comment\nUPDATE t", "UPDATE"},
		{"/* block */\nDELETE FROM t", "DELETE"},
	}
	for _, tc := range cases {
		got := firstKeyword(tc.input)
		if got != tc.want {
			t.Errorf("firstKeyword(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestQueryTypeString(t *testing.T) {
	if QueryTypeRead.String() != "read" {
		t.Errorf("QueryTypeRead.String() = %q", QueryTypeRead.String())
	}
	if QueryTypeWrite.String() != "write" {
		t.Errorf("QueryTypeWrite.String() = %q", QueryTypeWrite.String())
	}
}

func TestStripComments(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"SELECT 1", "SELECT 1"},
		{"-- comment\nSELECT 1", "\nSELECT 1"},
		{"/* block */ SELECT 1", "  SELECT 1"},
		{"/* INSERT */ SELECT 1", "  SELECT 1"},
		{"SELECT -- inline\n1", "SELECT \n1"},
	}
	for _, tc := range cases {
		got := stripComments(tc.input)
		if got != tc.want {
			t.Errorf("stripComments(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestContainsWord(t *testing.T) {
	cases := []struct {
		sql  string
		kw   string
		want bool
	}{
		{"INSERT INTO t", "INSERT", true},
		{"INSERTIONS INTO t", "INSERT", false}, // prefix, not standalone
		{"MY_INSERT INTO t", "INSERT", false},  // suffix, not standalone
		{"WITH x AS (DELETE FROM t) SELECT * FROM x", "DELETE", true},
		{"SELECT * FROM t", "DELETE", false},
	}
	for _, tc := range cases {
		got := containsWord(tc.sql, tc.kw)
		if got != tc.want {
			t.Errorf("containsWord(%q, %q) = %v, want %v", tc.sql, tc.kw, got, tc.want)
		}
	}
}

func TestIsMultiStatement(t *testing.T) {
	cases := []struct {
		sql  string
		want bool
	}{
		{"SELECT 1", false},
		{"SELECT 1;", false},                    // trailing semicolon only
		{"SELECT 1; SELECT 2", true},
		{"INSERT INTO t VALUES (1); SELECT 1", true},
		{"-- comment; with semicolon\nSELECT 1", false}, // semicolon inside comment
	}
	for _, tc := range cases {
		got := isMultiStatement(tc.sql)
		if got != tc.want {
			t.Errorf("isMultiStatement(%q) = %v, want %v", tc.sql, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Transaction control statements
// ---------------------------------------------------------------------------

// TestParseQueryType_TransactionStatements verifies that all transaction
// control statements (BEGIN variants, COMMIT, ROLLBACK variants, SAVEPOINT,
// RELEASE SAVEPOINT, START TRANSACTION) are classified as writes so they are
// always sent to the primary.
func TestParseQueryType_TransactionStatements(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		// BEGIN variants
		{"begin", "BEGIN"},
		{"begin_work", "BEGIN WORK"},
		{"begin_transaction", "BEGIN TRANSACTION"},
		{"begin_read_only", "BEGIN READ ONLY"},
		{"begin_read_write", "BEGIN READ WRITE"},
		{"begin_repeatable_read", "BEGIN ISOLATION LEVEL REPEATABLE READ"},
		{"begin_serializable", "BEGIN ISOLATION LEVEL SERIALIZABLE"},
		{"begin_read_committed", "BEGIN ISOLATION LEVEL READ COMMITTED"},
		{"begin_read_uncommitted", "BEGIN ISOLATION LEVEL READ UNCOMMITTED"},
		// START TRANSACTION
		{"start_transaction", "START TRANSACTION"},
		{"start_transaction_rr", "START TRANSACTION ISOLATION LEVEL REPEATABLE READ"},
		// COMMIT variants
		{"commit", "COMMIT"},
		{"commit_work", "COMMIT WORK"},
		{"commit_transaction", "COMMIT TRANSACTION"},
		// ROLLBACK variants
		{"rollback", "ROLLBACK"},
		{"rollback_work", "ROLLBACK WORK"},
		{"rollback_transaction", "ROLLBACK TRANSACTION"},
		{"rollback_to", "ROLLBACK TO s1"},
		{"rollback_to_savepoint", "ROLLBACK TO SAVEPOINT s1"},
		// Savepoints
		{"savepoint", "SAVEPOINT s1"},
		{"release_savepoint", "RELEASE SAVEPOINT s1"},
		// Case insensitivity
		{"begin_lower", "begin"},
		{"commit_lower", "commit"},
		{"rollback_lower", "rollback"},
		{"savepoint_lower", "savepoint sp"},
		{"start_upper", "START TRANSACTION"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseQueryType(tc.sql)
			if got != QueryTypeWrite {
				t.Errorf("ParseQueryType(%q) = %v, want Write (primary)", tc.sql, got)
			}
		})
	}
}
