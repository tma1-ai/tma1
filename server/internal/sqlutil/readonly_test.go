package sqlutil

import (
	"strings"
	"testing"
)

func TestValidateSelectAccepts(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "SELECT 1", "SELECT 1"},
		{"lowercase", "select * from tma1_messages", "select * from tma1_messages"},
		{"trailing semicolon", "SELECT 1;", "SELECT 1"},
		{"trailing semicolon and space", "SELECT 1 ;  ", "SELECT 1"},
		// Comments are skipped for inspection but kept in the statement.
		{"leading line comment", "-- pick one\nSELECT 1", "-- pick one\nSELECT 1"},
		{"leading block comment", "/* drop */ SELECT 1", "/* drop */ SELECT 1"},
		{"semicolon in literal", "SELECT * FROM t WHERE content = 'a; b'", "SELECT * FROM t WHERE content = 'a; b'"},
		{"escaped quote in literal", "SELECT * FROM t WHERE content = 'it''s; ok'", "SELECT * FROM t WHERE content = 'it''s; ok'"},
		{"semicolon in comment", "SELECT 1 -- ; not a statement", "SELECT 1 -- ; not a statement"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ValidateSelect(c.in)
			if err != nil {
				t.Fatalf("ValidateSelect(%q) = error %v, want ok", c.in, err)
			}
			if got != c.want {
				t.Errorf("ValidateSelect(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestValidateSelectRejects(t *testing.T) {
	cases := []struct{ name, in string }{
		{"empty", "   "},
		{"comment only", "-- nothing here"},
		{"insert", "INSERT INTO t VALUES (1)"},
		{"delete", "DELETE FROM t"},
		{"drop", "DROP TABLE t"},
		{"alter", "ALTER TABLE t ADD COLUMN a INT"},
		{"admin", "ADMIN flush_table('t')"},
		{"show", "SHOW TABLES"},
		{"describe", "DESCRIBE tma1_messages"},
		{"cte", "WITH x AS (SELECT 1) SELECT * FROM x"},
		{"batch", "SELECT 1; DROP TABLE t"},
		{"batch behind comment", "SELECT 1; -- x\nDELETE FROM t"},
		{"comment hiding statement start", "/* SELECT */ DROP TABLE t"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, err := ValidateSelect(c.in); err == nil {
				t.Errorf("ValidateSelect(%q) = %q, want error", c.in, got)
			}
		})
	}
}

// A statement ending in a line comment must not comment out the
// wrapper's closing parenthesis.
func TestLimitedSelectSurvivesTrailingComment(t *testing.T) {
	got := LimitedSelect("SELECT 1 -- why", 100)
	if !strings.HasSuffix(got, "\n) LIMIT 101") {
		t.Errorf("LimitedSelect() = %q, want wrapper closed on its own line", got)
	}
}
