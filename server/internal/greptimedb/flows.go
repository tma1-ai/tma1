// Package greptimedb — flow initialization.
package greptimedb

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

//go:embed flows.sql
var flowsSQL string

// InitFlows runs the flows.sql DDL against the GreptimeDB HTTP SQL API.
// It is idempotent (all statements use IF NOT EXISTS).
// Flow creation (CREATE FLOW) failures are non-fatal — they are logged as warnings
// and skipped, since the source table may have a different schema (e.g. openclaw.*
// columns instead of gen_ai.*).
func InitFlows(httpPort int, logger *slog.Logger) error {
	sqlURL := fmt.Sprintf("http://localhost:%d/v1/sql", httpPort)

	// Split on semicolons and execute each statement individually.
	statements := splitSQL(flowsSQL)
	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if err := execSQL(sqlURL, stmt); err != nil {
			if isFlowStatement(stmt) {
				logger.Warn("flow creation skipped (source table may have different schema)", "error", err)
				continue
			}
			return fmt.Errorf("init flows: %w", err)
		}
	}
	logger.Info("flow aggregations initialized")
	return nil
}

// isFlowStatement returns true if the SQL statement is a CREATE FLOW statement.
func isFlowStatement(stmt string) bool {
	upper := strings.ToUpper(strings.TrimSpace(stmt))
	return strings.HasPrefix(upper, "CREATE FLOW") || strings.HasPrefix(upper, "CREATE OR REPLACE FLOW")
}

func execSQL(sqlURL, stmt string) error {
	form := url.Values{}
	form.Set("sql", stmt)

	resp, err := http.Post(sqlURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode())) //nolint:gosec
	if err != nil {
		return fmt.Errorf("exec sql: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("exec sql HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// modelPrice holds one row from tma1_model_pricing.
type modelPrice struct {
	Pattern     string
	Priority    int
	InputPrice  float64
	OutputPrice float64
}

const pricingTableDDL = `CREATE TABLE IF NOT EXISTS tma1_model_pricing (
    model_pattern STRING PRIMARY KEY,
    priority      INT32,
    input_price   DOUBLE,
    output_price  DOUBLE,
    ts            TIMESTAMP TIME INDEX DEFAULT '2024-01-01T00:00:00Z'
);`

// defaultPricing is the seed data inserted on first start.
var defaultPricing = []modelPrice{
	{"claude-opus-4-6", 10, 5.0, 25.0},
	{"claude-opus-4-5", 11, 5.0, 25.0},
	{"claude-opus-4-1", 12, 15.0, 75.0},
	{"claude-opus-4-0", 13, 15.0, 75.0},
	{"claude-3-opus", 14, 15.0, 75.0},
	{"claude-sonnet", 20, 3.0, 15.0},
	{"claude-haiku-4-5", 30, 1.0, 5.0},
	{"claude-3-5-haiku", 31, 1.0, 5.0},
	{"claude-3-haiku", 32, 0.25, 1.25},
	{"claude", 99, 3.0, 15.0},
	{"o1-pro", 100, 150.0, 600.0},
	{"o1-mini", 101, 0.55, 2.2},
	{"o1", 109, 15.0, 60.0},
	{"o4-mini", 110, 0.55, 2.2},
	{"o3-mini", 111, 0.55, 2.2},
	{"o3", 119, 2.0, 8.0},
	{"gpt-4o-mini", 120, 0.15, 0.6},
	{"gpt-4o", 129, 2.5, 10.0},
	{"gpt-4.1-nano", 130, 0.1, 0.4},
	{"gpt-4.1-mini", 131, 0.4, 1.6},
	{"gpt-4.1", 139, 2.0, 8.0},
	{"gpt-5-nano", 140, 0.05, 0.4},
	{"gpt-5-mini", 141, 0.25, 2.0},
	{"gpt-5", 149, 0.625, 5.0},
	{"gpt-3.5", 150, 0.5, 1.5},
	{"gemini-2.5-pro", 200, 1.25, 10.0},
	{"gemini-2.5-flash", 201, 0.3, 2.5},
	{"gemini-2.0-flash", 202, 0.1, 0.4},
	{"gemini", 299, 0.3, 2.5},
}

// SeedPricing inserts default model pricing if the table is empty.
func SeedPricing(httpPort int, logger *slog.Logger) error {
	sqlURL := fmt.Sprintf("http://localhost:%d/v1/sql", httpPort)
	if err := execSQL(sqlURL, pricingTableDDL); err != nil {
		return fmt.Errorf("ensure pricing table: %w", err)
	}

	count, err := queryScalarInt(sqlURL, "SELECT COUNT(*) FROM tma1_model_pricing")
	if err != nil {
		return fmt.Errorf("seed pricing: %w", err)
	}
	if count > 0 {
		logger.Info("model pricing already seeded", "rows", count)
		return nil
	}

	// Build a single INSERT with all rows.
	var sb strings.Builder
	sb.WriteString("INSERT INTO tma1_model_pricing (model_pattern, priority, input_price, output_price, ts) VALUES ")
	for i, p := range defaultPricing {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "('%s', %d, %g, %g, '2024-01-01T00:00:00Z')",
			p.Pattern, p.Priority, p.InputPrice, p.OutputPrice)
	}
	sb.WriteString(";")

	if err := execSQL(sqlURL, sb.String()); err != nil {
		return fmt.Errorf("seed pricing: %w", err)
	}
	logger.Info("model pricing seeded", "rows", len(defaultPricing))
	return nil
}

// InitCostFlow reads pricing from tma1_model_pricing and creates/replaces
// the cost flow with a dynamic CASE expression.
func InitCostFlow(httpPort int, logger *slog.Logger) error {
	sqlURL := fmt.Sprintf("http://localhost:%d/v1/sql", httpPort)

	prices, err := queryPricing(sqlURL)
	if err != nil {
		return fmt.Errorf("init cost flow: %w", err)
	}

	costExpr := buildCostCaseSQL(prices,
		`"span_attributes.gen_ai.request.model"`,
		`"span_attributes.gen_ai.usage.input_tokens"`,
		`"span_attributes.gen_ai.usage.output_tokens"`,
	)

	flowSQL := fmt.Sprintf(`CREATE OR REPLACE FLOW tma1_cost_flow
SINK TO tma1_cost_1m
EXPIRE AFTER '7d'
COMMENT 'Estimated cost per model per minute (pricing from tma1_model_pricing)'
AS
SELECT
    "span_attributes.gen_ai.request.model" AS model,
    SUM(%s) AS cost_usd,
    date_bin('1 minute'::INTERVAL, "timestamp") AS time_window
FROM opentelemetry_traces
WHERE "span_attributes.gen_ai.system" IS NOT NULL
GROUP BY "span_attributes.gen_ai.request.model", time_window;`, costExpr)

	if err := execSQL(sqlURL, flowSQL); err != nil {
		logger.Warn("cost flow creation skipped (source table may have different schema)", "error", err)
		return nil
	}
	logger.Info("cost flow initialized with dynamic pricing", "models", len(prices))
	return nil
}

// buildCostCaseSQL generates a SQL CASE expression that computes cost
// based on model pattern matching via LIKE.
func buildCostCaseSQL(prices []modelPrice, modelExpr, inputExpr, outputExpr string) string {
	var sb strings.Builder
	sb.WriteString("CASE")
	for _, p := range prices {
		fmt.Fprintf(&sb, " WHEN %s LIKE '%%%s%%' THEN CAST(%s AS DOUBLE)*%.6f/1000000.0+CAST(%s AS DOUBLE)*%.6f/1000000.0",
			modelExpr, p.Pattern, inputExpr, p.InputPrice, outputExpr, p.OutputPrice)
	}
	// Default: Sonnet-tier ($3/$15)
	fmt.Fprintf(&sb, " ELSE CAST(%s AS DOUBLE)*3.000000/1000000.0+CAST(%s AS DOUBLE)*15.000000/1000000.0 END",
		inputExpr, outputExpr)
	return sb.String()
}

// queryScalarInt executes a query expected to return a single integer value.
func queryScalarInt(sqlURL, stmt string) (int, error) {
	form := url.Values{}
	form.Set("sql", stmt)

	resp, err := http.Post(sqlURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode())) //nolint:gosec
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Output []struct {
			Records struct {
				Rows [][]json.Number `json:"rows"`
			} `json:"records"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, fmt.Errorf("parse response: %w", err)
	}
	if len(result.Output) == 0 || len(result.Output[0].Records.Rows) == 0 ||
		len(result.Output[0].Records.Rows[0]) == 0 {
		return 0, nil
	}
	val, err := result.Output[0].Records.Rows[0][0].Int64()
	if err != nil {
		return 0, fmt.Errorf("parse count: %w", err)
	}
	return int(val), nil
}

// queryPricing reads all rows from tma1_model_pricing ordered by priority.
func queryPricing(sqlURL string) ([]modelPrice, error) {
	form := url.Values{}
	form.Set("sql", "SELECT model_pattern, priority, input_price, output_price FROM tma1_model_pricing ORDER BY priority")

	resp, err := http.Post(sqlURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode())) //nolint:gosec
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Output []struct {
			Records struct {
				Rows [][]any `json:"rows"`
			} `json:"records"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if len(result.Output) == 0 {
		return nil, nil
	}

	var prices []modelPrice
	for _, row := range result.Output[0].Records.Rows {
		if len(row) < 4 {
			continue
		}
		pattern, _ := row[0].(string)
		priority := toInt(row[1])
		inputPrice := toFloat(row[2])
		outputPrice := toFloat(row[3])
		prices = append(prices, modelPrice{
			Pattern:     pattern,
			Priority:    priority,
			InputPrice:  inputPrice,
			OutputPrice: outputPrice,
		})
	}
	return prices, nil
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	default:
		return 0
	}
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

// splitSQL splits a SQL file into individual statements on semicolons,
// skipping comment-only lines.
func splitSQL(s string) []string {
	var stmts []string
	var cur strings.Builder
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
		if strings.HasSuffix(trimmed, ";") {
			stmts = append(stmts, cur.String())
			cur.Reset()
		}
	}
	return stmts
}
