package greptimedb

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSplitSQL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int                   // expected number of statements
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
			name:  "statements without trailing newline",
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
	// Session tables (tma1_hook_events, tma1_messages) are in InitSessionTables, not flows.sql.
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
	t.Run("empty prices stay unknown", func(t *testing.T) {
		if result := buildCostCaseSQL(nil, "model", "inp", "outp"); result != "CAST(NULL AS DOUBLE)" {
			t.Fatal(result)
		}
	})

	t.Run("single price generates WHEN clause", func(t *testing.T) {
		prices := []modelPrice{{Pattern: "claude-opus", InputPrice: 5.0, OutputPrice: 25.0}}
		result := buildCostCaseSQL(prices, "m", "i", "o")
		if !strings.Contains(result, "WHEN regexp_like(m, '^claude-opus") {
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

func TestValidTTL(t *testing.T) {
	valid := []string{"60d", "1h", "30m", "7d", "12M", "1y", "3600s", "2w", "forever"}
	for _, v := range valid {
		if v != "forever" && !validTTL.MatchString(v) {
			t.Errorf("expected %q to be valid", v)
		}
	}

	invalid := []string{"", "abc", "60", "d60", "60dd", "60d; DROP TABLE x", "1 h", "-1d"}
	for _, v := range invalid {
		if v == "forever" || validTTL.MatchString(v) {
			t.Errorf("expected %q to be invalid", v)
		}
	}
}

func TestBuildCostCaseSQLLeavesLongPromptsUnpriced(t *testing.T) {
	prices := []modelPrice{{Pattern: "gpt-x", InputPrice: 1, OutputPrice: 2, MaxInputTokens: maxInput(272000)}}
	result := buildCostCaseSQL(prices, "m", "i", "o")
	if !strings.Contains(result, "regexp_like(m, '^gpt-x") || !strings.Contains(result, "AND CAST(i AS BIGINT) <= 272000 THEN") {
		t.Fatalf("long prompts must fall through to NULL: %s", result)
	}
}

func TestDefaultPricing(t *testing.T) {
	// Verify seed data is well-formed.
	if len(defaultPricing) == 0 {
		t.Fatal("defaultPricing should not be empty")
	}

	seen := make(map[string]bool)
	for _, p := range defaultPricing {
		key := p.Pattern + "/" + p.Tier
		if p.Pattern == "" {
			t.Error("empty pattern")
		}
		if p.Tier != pricingTierStandard && p.Tier != pricingTierPriority {
			t.Errorf("%s: unknown tier", key)
		}
		if seen[key] {
			t.Errorf("duplicate catalog row: %s", key)
		}
		seen[key] = true
		if p.InputPrice <= 0 || p.OutputPrice <= 0 {
			t.Errorf("%s: input and output prices must be positive", key)
		}
	}
	for _, p := range defaultPricing {
		if p.Tier == pricingTierPriority && !seen[p.Pattern+"/"+pricingTierStandard] {
			t.Errorf("%s has a priority row but no standard row", p.Pattern)
		}
	}
}

// TestExecSQLSurfacesJSONBodyError guards against the silent-failure
// regression where GreptimeDB returns HTTP 200 with a JSON error body
// and execSQL returns nil. If that ever returns nil again,
// RunSchemaMigrations stamps a failed migration as applied and the
// schema goes out of sync with the ledger.
func TestExecSQLSurfacesJSONBodyError(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		status  int
		wantErr bool
		wantSub string
	}{
		{
			name:    "json error code with HTTP 200",
			body:    `{"code":1003,"error":"column conversation_id already exists"}`,
			status:  http.StatusOK,
			wantErr: true,
			wantSub: "already exists",
		},
		{
			name:    "json error string only",
			body:    `{"error":"unknown column \"foo\""}`,
			status:  http.StatusOK,
			wantErr: true,
			wantSub: "unknown column",
		},
		{
			name:    "json error with leading whitespace",
			body:    "  \n\t" + `{"code":1003,"error":"duplicate column"}`,
			status:  http.StatusOK,
			wantErr: true,
			wantSub: "duplicate column",
		},
		{
			name:    "json code 0 = success",
			body:    `{"code":0,"output":[]}`,
			status:  http.StatusOK,
			wantErr: false,
		},
		{
			name:    "empty body 200 = success",
			body:    ``,
			status:  http.StatusOK,
			wantErr: false,
		},
		{
			name:    "http error",
			body:    `internal server error`,
			status:  http.StatusInternalServerError,
			wantErr: true,
			wantSub: "HTTP 500",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			err := execSQL(srv.URL, "ALTER TABLE t ADD COLUMN c STRING")
			if (err != nil) != tt.wantErr {
				t.Fatalf("execSQL err=%v wantErr=%v", err, tt.wantErr)
			}
			if tt.wantErr && tt.wantSub != "" && !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("execSQL err=%q does not contain %q", err.Error(), tt.wantSub)
			}
		})
	}
}

func TestIsIgnorableSchemaUpgradeError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "already exists", err: fmt.Errorf("column conversation_id already exists"), want: true},
		{name: "duplicate", err: fmt.Errorf("duplicate column name"), want: true},
		{name: "real failure", err: fmt.Errorf("HTTP 500: connection refused"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isIgnorableSchemaUpgradeError(tt.err); got != tt.want {
				t.Fatalf("isIgnorableSchemaUpgradeError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
