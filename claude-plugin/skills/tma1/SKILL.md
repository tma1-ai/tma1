---
name: tma1
description: "Query TMA1 observability data. Use when the user asks: how much did I spend, token usage, what has my agent been doing, agent cost, show me traces, show me events, check for errors, model comparison, tool usage."
context: fork
allowed-tools: Bash
---

# TMA1 Observability Query

You are helping the user query their local TMA1 observability data.

TMA1 stores data from three kinds of sources:
- **Claude Code** sends OTel **metrics** (cumulative counters) + **logs** (event stream)
- **OpenClaw** sends OTel **traces** (spans with openclaw.* attributes) + **metrics** (openclaw_* tables)
- **Other agents** (standard GenAI SDK) send OTel **traces** (spans with gen_ai.* semantic conventions)

## Step 1: Check TMA1 is running

```bash
curl -sf http://localhost:14318/health
```

If this fails, tell the user to run `/tma1-setup` first.

## Step 2: Detect available data sources

```bash
curl -s -X POST http://localhost:14318/api/query \
  -H 'Content-Type: application/json' \
  -d '{"sql": "SHOW TABLES"}'
```

Check which tables exist to determine what queries to use:
- If `claude_code_cost_usage_USD_total` exists → use Claude Code metrics queries
- If `openclaw_tokens_total` exists → use OpenClaw queries
- If `opentelemetry_traces` exists → use traces-based queries (check column names to distinguish OpenClaw vs GenAI)
- If `opentelemetry_logs` exists → use logs queries for event details

## Step 3: Choose and run query

All queries go through:

```bash
curl -s -X POST http://localhost:14318/api/query \
  -H 'Content-Type: application/json' \
  -d '{"sql": "<SQL>"}'
```

**Important**: GreptimeDB uses `json_get_string()`, `json_get_int()`, `json_get_float()` for JSON column access. The `->` / `->>` operators are NOT supported. Keys containing dots (like `session.id`) are interpreted as nested paths and cannot be accessed via `json_get_*`.

---

## Claude Code Queries (metrics + logs)

### Cost summary (latest snapshot per model)

The cost table contains cumulative counters reported every ~10s. To get the latest total cost per model, take the MAX value:

```sql
SELECT model,
       ROUND(MAX(greptime_value), 4) AS cost_usd
FROM "claude_code_cost_usage_USD_total"
WHERE greptime_timestamp >= DATE_TRUNC('day', NOW())
GROUP BY model
ORDER BY cost_usd DESC
```

### Token usage (latest snapshot per model per type)

The token table has `type` = input/output/cacheRead/cacheCreation. Values are cumulative counters:

```sql
SELECT model, type,
       MAX(greptime_value) AS tokens
FROM claude_code_token_usage_tokens_total
WHERE greptime_timestamp >= DATE_TRUNC('day', NOW())
GROUP BY model, type
ORDER BY model, type
```

### Recent API requests (from logs)

```sql
SELECT timestamp,
       json_get_string(log_attributes, 'model') AS model,
       json_get_int(log_attributes, 'input_tokens') AS input_tok,
       json_get_int(log_attributes, 'output_tokens') AS output_tok,
       json_get_float(log_attributes, 'cost_usd') AS cost_usd,
       json_get_float(log_attributes, 'duration_ms') AS duration_ms
FROM opentelemetry_logs
WHERE body = 'claude_code.api_request'
ORDER BY timestamp DESC
LIMIT 20
```

### API errors

```sql
SELECT timestamp,
       json_get_string(log_attributes, 'model') AS model,
       json_get_string(log_attributes, 'error') AS error,
       json_get_string(log_attributes, 'status_code') AS status_code,
       json_get_float(log_attributes, 'duration_ms') AS duration_ms
FROM opentelemetry_logs
WHERE body = 'claude_code.api_error'
ORDER BY timestamp DESC
LIMIT 20
```

### Tool usage (from logs)

```sql
SELECT json_get_string(log_attributes, 'tool_name') AS tool,
       COUNT(*) AS uses,
       ROUND(AVG(json_get_float(log_attributes, 'duration_ms'))) AS avg_ms,
       SUM(CASE WHEN json_get_string(log_attributes, 'success') = 'true' THEN 1 ELSE 0 END) AS ok,
       SUM(CASE WHEN json_get_string(log_attributes, 'success') = 'false' THEN 1 ELSE 0 END) AS fail
FROM opentelemetry_logs
WHERE body = 'claude_code.tool_result'
GROUP BY tool
ORDER BY uses DESC
```

### User prompts

```sql
SELECT timestamp,
       json_get_int(log_attributes, 'prompt_length') AS prompt_len
FROM opentelemetry_logs
WHERE body = 'claude_code.user_prompt'
ORDER BY timestamp DESC
LIMIT 20
```

### Active time

```sql
SELECT type,
       ROUND(MAX(greptime_value), 1) AS seconds
FROM claude_code_active_time_seconds_total
WHERE greptime_timestamp >= DATE_TRUNC('day', NOW())
GROUP BY type
```

### Lines of code

```sql
SELECT type,
       MAX(greptime_value) AS lines
FROM claude_code_lines_of_code_count_total
WHERE greptime_timestamp >= DATE_TRUNC('day', NOW())
GROUP BY type
```

### Model comparison

```sql
SELECT model,
       ROUND(MAX(greptime_value), 4) AS cost_usd
FROM "claude_code_cost_usage_USD_total"
GROUP BY model
ORDER BY cost_usd DESC
```

### Session-level cost over time

```sql
SELECT session_id, model,
       ROUND(MAX(greptime_value), 4) AS cost_usd,
       MIN(greptime_timestamp) AS started,
       MAX(greptime_timestamp) AS last_seen
FROM "claude_code_cost_usage_USD_total"
GROUP BY session_id, model
ORDER BY last_seen DESC
```

---

## OpenClaw Queries (traces + metrics)

These queries work when `openclaw_tokens_total` or `opentelemetry_traces` with openclaw.* attributes exist.

### Recent LLM calls

```sql
SELECT timestamp,
       "span_attributes.openclaw.model" AS model,
       "span_attributes.openclaw.channel" AS channel,
       CAST("span_attributes.openclaw.tokens.input" AS BIGINT) AS input_tok,
       CAST("span_attributes.openclaw.tokens.output" AS BIGINT) AS output_tok,
       CAST("span_attributes.openclaw.tokens.cache_read" AS BIGINT) AS cache_read,
       ROUND(duration_nano / 1000000.0, 1) AS duration_ms
FROM opentelemetry_traces
WHERE span_name = 'openclaw.model.usage'
ORDER BY timestamp DESC
LIMIT 20
```

### Token usage by model (from metrics)

```sql
SELECT model, token_type, SUM(greptime_value) AS tokens
FROM openclaw_tokens_total
WHERE ts > NOW() - INTERVAL '1 day'
GROUP BY model, token_type
ORDER BY tokens DESC
```

### Messages by channel

```sql
SELECT "span_attributes.openclaw.channel" AS channel,
       "span_attributes.openclaw.outcome" AS outcome,
       COUNT(*) AS messages
FROM opentelemetry_traces
WHERE span_name = 'openclaw.message.processed'
  AND timestamp > NOW() - INTERVAL '1 day'
GROUP BY channel, outcome
ORDER BY messages DESC
```

### Error spans

```sql
SELECT timestamp, span_name,
       "span_attributes.openclaw.channel" AS channel,
       "span_attributes.openclaw.sessionKey" AS session
FROM opentelemetry_traces
WHERE span_name IN ('openclaw.webhook.error', 'openclaw.session.stuck')
ORDER BY timestamp DESC
LIMIT 20
```

### Cost estimate (from traces)

```sql
SELECT "span_attributes.openclaw.model" AS model,
       COUNT(*) AS requests,
       SUM(CAST("span_attributes.openclaw.tokens.input" AS BIGINT)) AS input_tok,
       SUM(CAST("span_attributes.openclaw.tokens.output" AS BIGINT)) AS output_tok
FROM opentelemetry_traces
WHERE span_name = 'openclaw.model.usage'
  AND timestamp > NOW() - INTERVAL '1 day'
GROUP BY model
ORDER BY input_tok DESC
```

---

## Codex Queries (logs + traces + metrics)

These queries work when Codex telemetry is flowing into `opentelemetry_logs`,
`opentelemetry_traces`, or native `codex_*` metric tables.

### Recent API requests

```sql
SELECT timestamp,
       COALESCE(json_get_string(log_attributes, 'model'), 'unknown') AS model,
       COALESCE(json_get_int(log_attributes, 'input_token_count'), 0) AS input_tok,
       COALESCE(json_get_int(log_attributes, 'output_token_count'), 0) AS output_tok,
       COALESCE(json_get_int(log_attributes, 'cached_token_count'), 0) AS cached_tok,
       json_get_float(log_attributes, 'duration_ms') AS duration_ms
FROM opentelemetry_logs
WHERE scope_name LIKE 'codex_%'
  AND json_get_int(log_attributes, 'input_token_count') IS NOT NULL
ORDER BY timestamp DESC
LIMIT 20
```

### Requests by model (from native metrics)

```sql
SELECT model,
       SUM(greptime_value) AS requests
FROM codex_websocket_request_total
WHERE greptime_timestamp > NOW() - INTERVAL '1 day'
GROUP BY model
ORDER BY requests DESC
```

### Tool performance (from native metrics)

```sql
SELECT tool,
       success,
       SUM(greptime_value) AS calls
FROM codex_tool_call_total
WHERE greptime_timestamp > NOW() - INTERVAL '1 day'
GROUP BY tool, success
ORDER BY calls DESC
```

### Average TTFT by model

```sql
SELECT s.model,
       ROUND(SUM(s.greptime_value) / NULLIF(SUM(c.greptime_value), 0), 2) AS avg_ttft_ms
FROM codex_turn_ttft_duration_ms_milliseconds_sum s
JOIN codex_turn_ttft_duration_ms_milliseconds_count c
  ON s.model = c.model
 AND s.service_version = c.service_version
 AND s.greptime_timestamp = c.greptime_timestamp
WHERE s.greptime_timestamp > NOW() - INTERVAL '1 day'
GROUP BY s.model
ORDER BY avg_ttft_ms DESC
```

---

## GenAI Traces Queries (other agents)

These queries only work when `opentelemetry_traces` exists with gen_ai.* attributes.

### Recent traces

```sql
SELECT span_name,
       "span_attributes.gen_ai.request.model" AS model,
       "span_attributes.gen_ai.usage.input_tokens" AS input_tok,
       "span_attributes.gen_ai.usage.output_tokens" AS output_tok,
       duration_nano / 1000000 AS duration_ms,
       timestamp
FROM opentelemetry_traces
WHERE "span_attributes.gen_ai.system" IS NOT NULL
ORDER BY timestamp DESC
LIMIT 20
```

### Cost by model (from raw traces)

```sql
SELECT "span_attributes.gen_ai.request.model" AS model,
       ROUND(SUM(CASE
         WHEN "span_attributes.gen_ai.request.model" LIKE '%claude-3-5-sonnet%' THEN
           CAST("span_attributes.gen_ai.usage.input_tokens" AS DOUBLE)*3/1e6 + CAST("span_attributes.gen_ai.usage.output_tokens" AS DOUBLE)*15/1e6
         WHEN "span_attributes.gen_ai.request.model" LIKE '%gpt-4o%' THEN
           CAST("span_attributes.gen_ai.usage.input_tokens" AS DOUBLE)*2.5/1e6 + CAST("span_attributes.gen_ai.usage.output_tokens" AS DOUBLE)*10/1e6
         ELSE CAST("span_attributes.gen_ai.usage.input_tokens" AS DOUBLE)*1/1e6 + CAST("span_attributes.gen_ai.usage.output_tokens" AS DOUBLE)*3/1e6
       END), 4) AS cost_usd
FROM opentelemetry_traces
WHERE "span_attributes.gen_ai.system" IS NOT NULL
  AND timestamp >= DATE_TRUNC('day', NOW())
GROUP BY model
ORDER BY cost_usd DESC
```

### Token usage by model

```sql
SELECT "span_attributes.gen_ai.request.model" AS model,
       SUM(CAST("span_attributes.gen_ai.usage.input_tokens" AS DOUBLE)) AS input_tok,
       SUM(CAST("span_attributes.gen_ai.usage.output_tokens" AS DOUBLE)) AS output_tok,
       COUNT(*) AS requests
FROM opentelemetry_traces
WHERE "span_attributes.gen_ai.system" IS NOT NULL
  AND timestamp >= DATE_TRUNC('day', NOW())
GROUP BY model
ORDER BY input_tok DESC
```

### Error rate

```sql
SELECT "span_attributes.gen_ai.request.model" AS model,
       COUNT(*) AS requests,
       SUM(CASE WHEN span_status_code = 'STATUS_CODE_ERROR' THEN 1 ELSE 0 END) AS errors
FROM opentelemetry_traces
WHERE "span_attributes.gen_ai.system" IS NOT NULL
  AND timestamp > NOW() - INTERVAL '24 hours'
GROUP BY model
ORDER BY errors DESC
```

---

## Step 4: Execute and format

Run the chosen query via curl. Parse the JSON response and present it as a readable table or summary.

If a table does not exist (error code 4001), skip that query and try the alternative data source.

If the query returns no rows, explain that there may not be data for the requested time range.

## Step 5: Offer follow-ups

After presenting results, suggest related queries the user might want:
- "Want to see the breakdown by model?"
- "Should I check for API errors?"
- "Want to see tool usage stats?"
- "Want to compare sessions?"

Remind the user that the full dashboard is available at http://localhost:14318.
