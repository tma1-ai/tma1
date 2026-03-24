package greptimedb

import (
	"strings"
	"testing"
)

func TestValidTTL(t *testing.T) {
	valid := []string{"60d", "30d", "7d", "1h", "24h", "1m", "1s", "1w", "1M", "1y", "forever"}
	for _, v := range valid {
		if !validTTL.MatchString(v) {
			t.Errorf("validTTL.MatchString(%q) = false, want true", v)
		}
	}

	invalid := []string{"", "d", "60", "60 d", "60days", "60D", "'60d'", "60d; DROP TABLE foo", "-1d"}
	for _, v := range invalid {
		if validTTL.MatchString(v) {
			t.Errorf("validTTL.MatchString(%q) = true, want false", v)
		}
	}
}

func TestSplitSQL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int    // expected number of statements
		check func([]string) string // optional: return error message
	}{
		{
			name:  "empty",
			input: "",
			want:  0,
		},
		{
			name:  "comments only",
			input: "-- this is a comment\n-- another comment\n",
			want:  0,
		},
		{
			name:  "single statement",
			input: "SELECT 1;",
			want:  1,
		},
		{
			name:  "two statements",
			input: "SELECT 1;\nSELECT 2;",
			want:  2,
		},
		{
			name:  "multiline statement",
			input: "CREATE TABLE foo (\n  id INT\n);",
			want:  1,
		},
		{
			name: "mixed comments and statements",
			input: `-- Comment
CREATE TABLE foo (id INT);

-- Another comment
CREATE TABLE bar (id INT);
`,
			want: 2,
		},
		{
			name: "statements without trailing newline",
			input: "SELECT 1;\nSELECT 2;",
			want:  2,
		},
		{
			name: "skips comment lines in multi-line",
			input: `CREATE TABLE foo (
-- this is an inline comment
  id INT
);`,
			want: 1,
			check: func(stmts []string) string {
				// The comment line should be stripped.
				if strings.Contains(stmts[0], "inline comment") {
					return "comment line should be removed from statement"
				}
				return ""
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitSQL(tt.input)
			if len(got) != tt.want {
				t.Errorf("splitSQL() returned %d statements, want %d", len(got), tt.want)
				for i, s := range got {
					t.Logf("  [%d]: %q", i, s)
				}
				return
			}
			if tt.check != nil {
				if msg := tt.check(got); msg != "" {
					t.Error(msg)
				}
			}
		})
	}
}

func TestSplitSQLFlowsFile(t *testing.T) {
	// Verify that the embedded flows.sql parses correctly.
	stmts := splitSQL(flowsSQL)

	// We expect 8 statements: 5 CREATE TABLE (4 sink + 1 pricing) + 3 CREATE FLOW
	if len(stmts) != 8 {
		t.Errorf("flows.sql: got %d statements, want 8", len(stmts))
		for i, s := range stmts {
			t.Logf("  [%d]: %s", i, strings.TrimSpace(s)[:min(80, len(strings.TrimSpace(s)))])
		}
	}

	// Each statement should be non-empty after trim.
	for i, s := range stmts {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			t.Errorf("statement %d is empty", i)
		}
	}
}

func TestBuildCostCaseSQL(t *testing.T) {
	t.Run("empty prices uses default only", func(t *testing.T) {
		result := buildCostCaseSQL(nil, "model", "inp", "outp")
		if !strings.HasPrefix(result, "CASE ELSE") {
			t.Errorf("expected CASE ELSE for empty prices, got: %s", result[:min(60, len(result))])
		}
		if !strings.Contains(result, "3.000000/1000000.0") {
			t.Error("expected default input price 3.0")
		}
		if !strings.Contains(result, "15.000000/1000000.0") {
			t.Error("expected default output price 15.0")
		}
	})

	t.Run("single price generates WHEN clause", func(t *testing.T) {
		prices := []modelPrice{{Pattern: "claude-opus", InputPrice: 5.0, OutputPrice: 25.0}}
		result := buildCostCaseSQL(prices, "m", "i", "o")
		if !strings.Contains(result, "WHEN m LIKE '%claude-opus%'") {
			t.Errorf("expected LIKE clause, got: %s", result)
		}
		if !strings.Contains(result, "5.000000/1000000.0") {
			t.Error("expected input price 5.0")
		}
		if !strings.Contains(result, "25.000000/1000000.0") {
			t.Error("expected output price 25.0")
		}
		if !strings.Contains(result, "ELSE") {
			t.Error("expected ELSE clause")
		}
	})

	t.Run("multiple prices in order", func(t *testing.T) {
		prices := []modelPrice{
			{Pattern: "opus", InputPrice: 15.0, OutputPrice: 75.0},
			{Pattern: "sonnet", InputPrice: 3.0, OutputPrice: 15.0},
		}
		result := buildCostCaseSQL(prices, "model", "inp", "outp")
		opusIdx := strings.Index(result, "opus")
		sonnetIdx := strings.Index(result, "sonnet")
		if opusIdx < 0 || sonnetIdx < 0 {
			t.Fatal("both patterns should appear")
		}
		if opusIdx > sonnetIdx {
			t.Error("opus should appear before sonnet (priority order)")
		}
	})
}

func TestIsFlowStatement(t *testing.T) {
	tests := []struct {
		stmt string
		want bool
	}{
		{"CREATE FLOW tma1_token_flow SINK TO ...", true},
		{"CREATE OR REPLACE FLOW tma1_cost_flow SINK TO ...", true},
		{"  create flow foo ...", true},
		{"CREATE TABLE IF NOT EXISTS tma1_token_usage_1m ...", false},
		{"SELECT 1;", false},
		{"", false},
	}
	for _, tt := range tests {
		got := isFlowStatement(tt.stmt)
		if got != tt.want {
			t.Errorf("isFlowStatement(%q) = %v, want %v", tt.stmt, got, tt.want)
		}
	}
}

func TestDefaultPricing(t *testing.T) {
	// Verify seed data is well-formed.
	if len(defaultPricing) == 0 {
		t.Fatal("defaultPricing should not be empty")
	}

	seen := make(map[string]bool)
	for _, p := range defaultPricing {
		if p.Pattern == "" {
			t.Error("empty pattern")
		}
		if seen[p.Pattern] {
			t.Errorf("duplicate pattern: %s", p.Pattern)
		}
		seen[p.Pattern] = true
		if p.InputPrice <= 0 {
			t.Errorf("pattern %s: input_price should be > 0", p.Pattern)
		}
		if p.OutputPrice <= 0 {
			t.Errorf("pattern %s: output_price should be > 0", p.Pattern)
		}
	}
}
