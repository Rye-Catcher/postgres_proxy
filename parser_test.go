package main

import (
	"testing"
)

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
