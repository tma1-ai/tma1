// Package greptimedb — flow initialization.
package greptimedb

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// validTTL matches GreptimeDB TTL values: digits + unit suffix, or "forever".
var validTTL = regexp.MustCompile(`^\d+[smhdwMy]$`)

var httpClient = &http.Client{Timeout: 30 * time.Second}

//go:embed flows.sql
var flowsSQL string

// SetDatabaseTTL sets the default TTL on the public database so that
// auto-created tables (OTel traces, logs, metrics) inherit it.
// Idempotent — safe to call on every startup.
func SetDatabaseTTL(httpPort int, ttl string, logger *slog.Logger) error {
	if ttl != "forever" && !validTTL.MatchString(ttl) {
		return fmt.Errorf("invalid TTL %q: must match <digits><unit> (e.g. 60d) or 'forever'", ttl)
	}
	sqlURL := fmt.Sprintf("http://localhost:%d/v1/sql", httpPort)
	stmt := fmt.Sprintf("ALTER DATABASE public SET 'ttl'='%s'", ttl)
	if err := execSQL(sqlURL, stmt); err != nil {
		return fmt.Errorf("set database TTL: %w", err)
	}
	logger.Info("database default TTL set", "ttl", ttl)
	return nil
}

// sessionTableDDLs are created unconditionally on startup (no dependency
// on trace data). Layout follows the GreptimeDB design-table guide:
//
//   - PRIMARY KEY: low-cardinality columns that appear in every
//     WHERE / GROUP BY / ORDER BY (agent_source + event_type for
//     hook_events; "role" + message_type for messages). With
//     append_mode=true, PK is purely a locality hint -- no dedupe
//     overhead, but data with the same PK clusters together for
//     better scan performance.
//   - SKIPPING INDEX on session_id (high cardinality UUID-ish) for
//     bloom-filter accelerated equality lookups.
//   - INVERTED INDEX on `model` (medium cardinality, used as a
//     filter sometimes in messages).
//   - FULLTEXT INDEX on content for matches_term() keyword search.
//
// All columns from v1 + v2 migrations (conversation_id,
// permission_mode, metadata, tool_file_path, tool_command_prefix,
// tool_success, tool_error_summary on hook_events; the token + timing
// columns on messages) are baked into the base DDL so the v3
// migration's DROP+CREATE lands the full schema in one shot. v1+v2
// ADD COLUMN migrations on subsequent startups become no-ops
// (already-exists errors swallowed by isIgnorableSchemaUpgradeError).
var sessionTableDDLs = []string{
	`CREATE TABLE IF NOT EXISTS tma1_hook_events (
    ts                  TIMESTAMP TIME INDEX,
    agent_source        STRING,
    event_type          STRING,
    session_id          STRING SKIPPING INDEX,
    tool_name           STRING NULL,
    tool_input          STRING NULL,
    tool_result         STRING NULL,
    tool_use_id         STRING NULL,
    agent_id            STRING NULL,
    agent_type          STRING NULL,
    notification_type   STRING NULL,
    "message"           STRING NULL,
    cwd                 STRING NULL,
    transcript_path     STRING NULL,
    conversation_id     STRING NULL,
    permission_mode     STRING NULL,
    "metadata"          STRING NULL,
    tool_file_path      STRING NULL,
    tool_command_prefix STRING NULL,
    tool_success        BOOLEAN NULL,
    tool_error_summary  STRING NULL,
    PRIMARY KEY (agent_source, event_type)
) WITH ('append_mode'='true')`,
	`CREATE TABLE IF NOT EXISTS tma1_messages (
    ts                    TIMESTAMP TIME INDEX,
    "role"                STRING,
    message_type          STRING,
    session_id            STRING SKIPPING INDEX,
    content               STRING NULL FULLTEXT INDEX WITH (backend='bloom', analyzer='English', case_sensitive='false'),
    model                 STRING NULL INVERTED INDEX,
    tool_name             STRING NULL,
    tool_use_id           STRING NULL,
    input_tokens          BIGINT NULL,
    output_tokens         BIGINT NULL,
    cache_read_tokens     BIGINT NULL,
    cache_creation_tokens BIGINT NULL,
    reasoning_tokens      BIGINT NULL,
    duration_ms           BIGINT NULL,
    PRIMARY KEY ("role", message_type)
) WITH ('append_mode'='true')`,
	`CREATE TABLE IF NOT EXISTS tma1_prompt_insights (
    ts              TIMESTAMP TIME INDEX,
    insight_id      STRING SKIPPING INDEX,
    summary         STRING NULL,
    patterns        STRING NULL,
    top_tip         STRING NULL,
    model           STRING NULL,
    sample_size     INT32 NULL,
    total_prompts   INT32 NULL,
    avg_score       INT32 NULL,
    sample_prompts  STRING NULL,
    time_range      STRING NULL
) WITH ('append_mode'='true')`,
}

// isIgnorableSchemaUpgradeError matches the GreptimeDB error surface for
// ALTER TABLE statements that target already-present columns. The
// schema_migrations IgnoreErr field wraps this so a DB that already
// has the v1/v2 columns (from the pre-ledger inline-tolerant ALTER
// path) can adopt the ledger without re-creating the table.
func isIgnorableSchemaUpgradeError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already exists") || strings.Contains(msg, "duplicate")
}

// InitSessionTables creates the session tables.
// Uses append-only mode with proper indexes for optimal performance.
//
// ALTER TABLE upgrades live in schema_migrations.go (RunSchemaMigrations) —
// keep this function focused on initial CREATE TABLE so the two paths
// can be reasoned about (and tested) independently.
func InitSessionTables(httpPort int, logger *slog.Logger) error {
	sqlURL := fmt.Sprintf("http://localhost:%d/v1/sql", httpPort)
	for _, ddl := range sessionTableDDLs {
		if err := execSQL(sqlURL, ddl); err != nil {
			return fmt.Errorf("init session tables: %w", err)
		}
	}
	logger.Info("session tables initialized")
	return nil
}

// InitFlows runs the flows.sql DDL against the GreptimeDB HTTP SQL API.
// It is idempotent (all statements use IF NOT EXISTS).
// Flow creation (CREATE FLOW) failures are non-fatal — they are logged as warnings
// and skipped, since the source table may have a different schema without gen_ai.* columns.
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

// expectedFlows is the set of flow names that should exist when fully initialized.
var expectedFlows = map[string]struct{}{
	"tma1_token_usage_flow": {},
	"tma1_latency_flow":     {},
	"tma1_status_flow":      {},
	"tma1_cost_flow":        {},
}

// FlowsReady returns true if all expected flows already exist.
func FlowsReady(httpPort int) bool {
	sqlURL := fmt.Sprintf("http://localhost:%d/v1/sql", httpPort)
	form := url.Values{}
	form.Set("sql", "SHOW FLOWS")

	resp, err := httpClient.Post(sqlURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode())) //nolint:gosec
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return false
	}

	var result struct {
		Output []struct {
			Records struct {
				Rows [][]string `json:"rows"`
			} `json:"records"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return false
	}

	found := make(map[string]struct{})
	if len(result.Output) > 0 {
		for _, row := range result.Output[0].Records.Rows {
			if len(row) > 0 {
				found[row[0]] = struct{}{}
			}
		}
	}
	for name := range expectedFlows {
		if _, ok := found[name]; !ok {
			return false
		}
	}
	return true
}

// HasGenAITraces returns true if opentelemetry_traces contains at least one
// GenAI span (gen_ai.system or gen_ai.provider.name is set).
// Returns false if the table does not exist or has no GenAI data.
func HasGenAITraces(httpPort int) bool {
	sqlURL := fmt.Sprintf("http://localhost:%d/v1/sql", httpPort)
	// Try the current attribute first, then fall back to the deprecated one.
	for _, col := range []string{"span_attributes.gen_ai.provider.name", "span_attributes.gen_ai.system"} {
		n, err := queryScalarInt(sqlURL,
			fmt.Sprintf(`SELECT 1 FROM opentelemetry_traces WHERE "%s" IS NOT NULL LIMIT 1`, col))
		if err == nil && n > 0 {
			return true
		}
	}
	return false
}

// isFlowStatement returns true if the SQL statement is a CREATE FLOW statement.
func isFlowStatement(stmt string) bool {
	upper := strings.ToUpper(strings.TrimSpace(stmt))
	return strings.HasPrefix(upper, "CREATE FLOW") || strings.HasPrefix(upper, "CREATE OR REPLACE FLOW")
}

func execSQL(sqlURL, stmt string) error {
	form := url.Values{}
	form.Set("sql", stmt)

	resp, err := httpClient.Post(sqlURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode())) //nolint:gosec
	if err != nil {
		return fmt.Errorf("exec sql: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("exec sql HTTP %d: %s", resp.StatusCode, string(body))
	}
	// GreptimeDB's HTTP SQL API returns SQL errors as HTTP 200 with a
	// JSON body whose `code` is non-zero. Without this check, ALTER /
	// CREATE failures slip past and the schema-migration ledger stamps
	// the version as applied even though the statement never ran.
	// Look at the trimmed slice so a body that happens to start with
	// whitespace still gets parsed.
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var r struct {
			Code  int    `json:"code"`
			Error string `json:"error"`
		}
		if json.Unmarshal(trimmed, &r) == nil && (r.Code != 0 || r.Error != "") {
			msg := r.Error
			if msg == "" {
				msg = fmt.Sprintf("code %d", r.Code)
			}
			return fmt.Errorf("exec sql: %s", msg)
		}
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

// PricingVersion identifies the built-in catalog independently of the binary version.
const PricingVersion = "2026-10-30.2"

// Standard, short-context API rates in USD per million tokens. These are
// estimates, not invoices; cache writes, service tiers and regional premiums
// require billing metadata that transcripts do not consistently supply.
// Sources: https://developers.openai.com/api/docs/pricing
// https://platform.claude.com/docs/en/about-claude/pricing
// Unverified models and context-tiered Haiku 5.5 are deliberately absent.
var defaultPricing = []modelPrice{
	{"claude-fable-5-1", 0, 10, 50},
	{"claude-opus-5-5", 1, 4, 20},
	{"claude-mythos-5-1", 3, 10, 50},
	{"claude-fable-5", 4, 10, 50},
	{"claude-mythos-5", 5, 10, 50},
	{"claude-opus-5", 6, 5, 25},
	{"claude-opus-4-8", 7, 5, 25},
	{"claude-opus-4-7", 8, 5, 25},
	{"claude-opus-4-6", 9, 5, 25},
	{"claude-opus-4-5", 10, 5, 25},
	{"claude-opus-4-1", 11, 15, 75},
	{"claude-opus-4", 12, 15, 75},
	{"claude-opus-4-0", 13, 15, 75},
	{"claude-sonnet-5", 14, 2, 10},
	{"claude-sonnet-4-6", 15, 3, 15},
	{"claude-sonnet-4-5", 16, 3, 15},
	{"claude-sonnet-4", 17, 3, 15},
	{"claude-sonnet-4-0", 18, 3, 15},
	{"claude-haiku-4-5", 19, 1, 5},
	{"claude-3-5-haiku", 20, 0.8, 4},
	{"gpt-6-astra", 21, 10, 50},
	{"gpt-6.1-sol", 22, 2, 10},
	{"gpt-6.1-terra", 23, 1, 5},
	{"gpt-6-sol", 24, 2, 10},
	{"gpt-6-luna", 25, 0.1, 0.5},
	{"gpt-5.6-sol", 26, 4, 20},
	{"gpt-5.6-cyber", 27, 12.5, 75},
	{"gpt-5.5", 28, 5, 30},
	{"gpt-5.3-codex", 29, 1.75, 14},
	{"gpt-4.1", 30, 2, 8},
	{"gpt-5.4", 31, 2.5, 15},
	{"gpt-5.2", 32, 1.75, 14},
	{"gpt-5.4-mini", 33, .75, 4.5},
	{"gpt-5-mini", 34, .25, 2},
	{"gpt-4o-mini", 35, .15, .6},
	{"gpt-4o", 36, 2.5, 10},
}

// SeedPricing inserts default model pricing if the table is empty.
func SeedPricing(httpPort int, logger *slog.Logger) error {
	sqlURL := fmt.Sprintf("http://localhost:%d/v1/sql", httpPort)
	if err := execSQL(sqlURL, pricingTableDDL); err != nil {
		return fmt.Errorf("ensure pricing table: %w", err)
	}
	// Rows use a fixed 2024 timestamp. With the inherited database TTL,
	// compaction deletes the whole catalog shortly after seeding.
	if err := execSQL(sqlURL, "ALTER TABLE tma1_model_pricing SET 'ttl'='forever'"); err != nil {
		return fmt.Errorf("keep pricing table: %w", err)
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

// TruncatePricing removes all rows from the pricing table so that
// SeedPricing can re-insert the latest defaults on upgrade.
func TruncatePricing(httpPort int) error {
	sqlURL := fmt.Sprintf("http://localhost:%d/v1/sql", httpPort)
	return execSQL(sqlURL, "TRUNCATE TABLE tma1_model_pricing")
}

// IsTableNotFound returns true if the error indicates the table does not exist.
func IsTableNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not found")
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
    CASE WHEN COUNT(*) = COUNT(%[1]s) THEN SUM(%[1]s) ELSE NULL END AS cost_usd,
    date_bin('1 minute'::INTERVAL, "timestamp") AS time_window
FROM opentelemetry_traces
WHERE "span_attributes.gen_ai.request.model" IS NOT NULL
GROUP BY "span_attributes.gen_ai.request.model", time_window;`, costExpr)

	if err := execSQL(sqlURL, flowSQL); err != nil {
		return fmt.Errorf("cost flow creation failed: %w", err)
	}
	logger.Info("cost flow initialized with dynamic pricing", "models", len(prices))
	return nil
}

// modelPricePattern accepts an exact ID or a provider's dated snapshot suffix.
func modelPricePattern(model string) string {
	return "^" + regexp.QuoteMeta(model) + "(-[0-9]{8}|-[0-9]{4}-[0-9]{2}-[0-9]{2})?$"
}

func buildCostCaseSQL(prices []modelPrice, modelExpr, inputExpr, outputExpr string) string {
	if len(prices) == 0 {
		return "CAST(NULL AS DOUBLE)"
	}
	var sb strings.Builder
	sb.WriteString("CASE")
	for _, p := range prices {
		safe := strings.ReplaceAll(modelPricePattern(p.Pattern), "'", "''")
		fmt.Fprintf(&sb, " WHEN regexp_like(%s, '%s') THEN CAST(%s AS DOUBLE)*%.6f/1000000.0+CAST(%s AS DOUBLE)*%.6f/1000000.0", modelExpr, safe, inputExpr, p.InputPrice, outputExpr, p.OutputPrice)
	}
	sb.WriteString(" ELSE CAST(NULL AS DOUBLE) END")
	return sb.String()
}

// queryScalarInt executes a query expected to return a single integer value.
func queryScalarInt(sqlURL, stmt string) (int, error) {
	form := url.Values{}
	form.Set("sql", stmt)

	resp, err := httpClient.Post(sqlURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode())) //nolint:gosec
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

	resp, err := httpClient.Post(sqlURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode())) //nolint:gosec
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
