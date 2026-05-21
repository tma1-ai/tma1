---
name: tma1
description: "Query TMA1 observability data (Claude Code, Codex, GitHub Copilot CLI, OpenClaw, any OTel SDK). Use when the user asks: how much did I spend, token usage, what has my agent been doing, agent cost, show me traces, show me events, check for errors, model comparison, tool usage."
context: fork
allowed-tools: Bash
---

# TMA1 Observability Query

You are helping the user query their local TMA1 observability data.

TMA1 stores data from five kinds of sources:
- **Claude Code** sends OTel **metrics** (cumulative counters) + **logs** (event stream) + hooks + JSONL transcripts
- **Codex** sends OTel **logs** + **metrics** + session JSONL (auto-parsed from `~/.codex/sessions/`)
- **GitHub Copilot CLI** — session JSONL only (auto-parsed from `~/.copilot/session-state/<sessionId>/events.jsonl`, no OTel). Data lives in `tma1_hook_events` (`agent_source='copilot_cli'`) and `tma1_messages` (`session_id LIKE 'cp:%'`).
- **OpenClaw** sends OTel **traces** (spans with openclaw.* attributes) + **metrics** (openclaw_* tables) + session JSONL (auto-parsed from `~/.openclaw/agents/*/sessions/`)
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
- If `codex_turn_token_usage_sum` or `codex_*` tables exist → use Codex queries
- If `openclaw_tokens_total` exists → use OpenClaw queries
- If `opentelemetry_traces` exists → use traces-based queries (check column names to distinguish OpenClaw vs GenAI)
- If `opentelemetry_logs` exists → use logs queries for event details
- If `tma1_hook_events` has rows with `agent_source = 'copilot_cli'` → use Copilot CLI queries
- If `tma1_hook_events` or `tma1_messages` exists → use session/conversation queries

## Step 3: Choose and run query

All queries go through:

```bash
curl -s -X POST http://localhost:14318/api/query \
  -H 'Content-Type: application/json' \
  -d '{"sql": "<SQL>"}'
```

**Important**: The underlying database (GreptimeDB) uses `json_get_string()`, `json_get_int()`, `json_get_float()` for JSON column access. The `->` / `->>` operators are NOT supported. Keys containing dots (like `session.id`) are interpreted as nested paths and cannot be accessed via `json_get_*`. All timestamps are stored and returned in **UTC** by default. To use the user's local timezone, add `-H 'X-Greptime-Timezone: <tz>'` (e.g. `+8:00`, `-5:00`, `Asia/Shanghai`, `America/New_York`) — this affects both date parsing in WHERE clauses and timestamp rendering in results.

---

## Claude Code Queries (logs + metrics)

**Note:** Claude Code resets OTel cumulative counters on each new session. Use **logs** (`opentelemetry_logs WHERE body = 'claude_code.api_request'`) for accurate cost/token totals. The `_total` metric tables only reflect the last session's counter value.

### Cost summary (from logs — accurate across sessions)

```sql
SELECT json_get_string(log_attributes, 'model') AS model,
       ROUND(SUM(json_get_float(log_attributes, 'cost_usd')), 4) AS cost_usd,
       COUNT(*) AS requests
FROM opentelemetry_logs
WHERE body = 'claude_code.api_request'
  AND timestamp >= DATE_TRUNC('day', NOW())
GROUP BY model
ORDER BY cost_usd DESC
```

### Token usage (from logs — accurate across sessions)

```sql
SELECT json_get_string(log_attributes, 'model') AS model,
       SUM(json_get_int(log_attributes, 'input_tokens')) AS input_tok,
       SUM(json_get_int(log_attributes, 'output_tokens')) AS output_tok,
       SUM(json_get_int(log_attributes, 'cache_read_tokens')) AS cache_read,
       SUM(json_get_int(log_attributes, 'cache_creation_tokens')) AS cache_write
FROM opentelemetry_logs
WHERE body = 'claude_code.api_request'
  AND timestamp >= DATE_TRUNC('day', NOW())
GROUP BY model
ORDER BY input_tok DESC
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

### Active time (last session only — counter resets per session)

```sql
SELECT type,
       ROUND(MAX(greptime_value), 1) AS seconds
FROM claude_code_active_time_seconds_total
WHERE greptime_timestamp >= DATE_TRUNC('day', NOW())
GROUP BY type
```

### Lines of code (last session only — counter resets per session)

```sql
SELECT type,
       MAX(greptime_value) AS lines
FROM claude_code_lines_of_code_count_total
WHERE greptime_timestamp >= DATE_TRUNC('day', NOW())
GROUP BY type
```

### Model comparison (all time)

```sql
SELECT json_get_string(log_attributes, 'model') AS model,
       ROUND(SUM(json_get_float(log_attributes, 'cost_usd')), 4) AS cost_usd,
       COUNT(*) AS requests
FROM opentelemetry_logs
WHERE body = 'claude_code.api_request'
GROUP BY model
ORDER BY cost_usd DESC
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
SELECT openclaw_model AS model, openclaw_token AS token_type, SUM(greptime_value) AS tokens
FROM openclaw_tokens_total
WHERE greptime_timestamp > NOW() - INTERVAL '1 day'
GROUP BY openclaw_model, openclaw_token
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

## Copilot CLI Queries (JSONL auto-discovery, no OTel)

Copilot CLI has no OTel exporter. All data comes from `~/.copilot/session-state/*/events.jsonl` and lands in:
- `tma1_hook_events` with `agent_source = 'copilot_cli'` — session lifecycle, tool calls, subagent lifecycle
- `tma1_messages` with `session_id LIKE 'cp:%'` — user / assistant / thinking messages

### Recent sessions

```sql
SELECT session_id,
       MIN(ts) AS started,
       MAX(ts) AS last_event,
       SUM(CASE WHEN event_type = 'PreToolUse' THEN 1 ELSE 0 END) AS tool_calls,
       SUM(CASE WHEN event_type = 'PostToolUseFailure' THEN 1 ELSE 0 END) AS tool_failures
FROM tma1_hook_events
WHERE agent_source = 'copilot_cli'
  AND ts > NOW() - INTERVAL '1 day'
GROUP BY session_id
ORDER BY last_event DESC
LIMIT 20
```

### Output tokens by model

```sql
SELECT model,
       SUM(COALESCE(output_tokens, 0)) AS output_tokens,
       COUNT(*) AS messages
FROM tma1_messages
WHERE session_id LIKE 'cp:%'
  AND model != ''
  AND ts > NOW() - INTERVAL '1 day'
GROUP BY model
ORDER BY output_tokens DESC
```

### Tool usage

```sql
SELECT tool_name,
       COUNT(*) AS calls
FROM tma1_hook_events
WHERE agent_source = 'copilot_cli'
  AND event_type = 'PreToolUse'
  AND tool_name != ''
  AND ts > NOW() - INTERVAL '1 day'
GROUP BY tool_name
ORDER BY calls DESC
LIMIT 15
```

### Subagent runs (model, tokens, duration in metadata)

```sql
SELECT ts, agent_type, metadata
FROM tma1_hook_events
WHERE agent_source = 'copilot_cli'
  AND event_type = 'SubagentStop'
ORDER BY ts DESC
LIMIT 20
```

### Conversation for a session

```sql
SELECT ts, message_type, "role", model, content
FROM tma1_messages
WHERE session_id = 'cp:<sessionId>'
ORDER BY ts ASC
```

Replace `<sessionId>` with the on-disk directory name from `~/.copilot/session-state/` (the `cp:` prefix is the DB namespace).

---

## GenAI Conversation Search (full-text)

Requires the OTel SDK to capture conversation content into `opentelemetry_logs`.
For Python with OpenAI, use `opentelemetry-instrumentation-openai-v2` and set
`OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true`.

The `openai_v2` instrumentation stores log bodies in these formats:
- User prompt: `{"content":"..."}`
- LLM completion: `{"message":{"role":"assistant","content":"..."}}`
- Tool result: `{"content":"...","id":"call_..."}`
- Tool call definition: `{"tool_calls":[...]}` (no displayable content)

### Search conversations by keyword

```sql
SELECT timestamp, trace_id,
  CASE
    WHEN json_get_string(parse_json(body), 'message.role') IS NOT NULL
      THEN json_get_string(parse_json(body), 'message.role')
    WHEN json_get_string(parse_json(body), 'id') IS NOT NULL THEN 'tool'
    WHEN json_get_string(parse_json(body), 'content') IS NOT NULL THEN 'user'
    ELSE 'unknown'
  END AS role,
  COALESCE(
    json_get_string(parse_json(body), 'message.content'),
    json_get_string(parse_json(body), 'content')
  ) AS content
FROM opentelemetry_logs
WHERE matches_term(body, 'your_keyword')
ORDER BY timestamp DESC LIMIT 50
```

### Conversation replay by trace_id

```sql
SELECT timestamp,
  CASE
    WHEN json_get_string(parse_json(body), 'message.role') IS NOT NULL
      THEN json_get_string(parse_json(body), 'message.role')
    WHEN json_get_string(parse_json(body), 'id') IS NOT NULL THEN 'tool'
    WHEN json_get_string(parse_json(body), 'content') IS NOT NULL THEN 'user'
    ELSE 'unknown'
  END AS role,
  COALESCE(
    json_get_string(parse_json(body), 'message.content'),
    json_get_string(parse_json(body), 'content')
  ) AS content
FROM opentelemetry_logs
WHERE trace_id = '<trace_id>'
ORDER BY timestamp LIMIT 100
```

### Prompt injection scan

```sql
SELECT timestamp, trace_id,
  json_get_string(parse_json(body), 'content') AS content
FROM opentelemetry_logs
WHERE trace_id != ''
  AND json_get_string(parse_json(body), 'content') IS NOT NULL
  AND json_get_string(parse_json(body), 'message.role') IS NULL
  AND json_get_string(parse_json(body), 'id') IS NULL
  AND (
    body LIKE '%ignore%previous%instructions%'
    OR body LIKE '%ignore%above%'
    OR body LIKE '%disregard%previous%'
    OR body LIKE '%reveal%system%prompt%'
    OR body LIKE '%jailbreak%'
    OR body LIKE '%DAN%mode%'
    OR body LIKE '%bypass%safety%'
    OR body LIKE '%[system]%'
  )
  AND timestamp > NOW() - INTERVAL '24 hours'
ORDER BY timestamp DESC LIMIT 50
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

### Cost by model (using TMA1's pricing table)

```sql
-- Joins against tma1_model_pricing (seeded by tma1-server on first start).
-- To see/edit pricing: SELECT * FROM tma1_model_pricing ORDER BY priority;
SELECT t.model,
       ROUND(SUM(
         CAST(t.input_tok AS DOUBLE) * p.input_price / 1e6 +
         CAST(t.output_tok AS DOUBLE) * p.output_price / 1e6
       ), 4) AS cost_usd
FROM (
  SELECT "span_attributes.gen_ai.request.model" AS model,
         "span_attributes.gen_ai.usage.input_tokens" AS input_tok,
         "span_attributes.gen_ai.usage.output_tokens" AS output_tok
  FROM opentelemetry_traces
  WHERE "span_attributes.gen_ai.system" IS NOT NULL
    AND timestamp >= DATE_TRUNC('day', NOW())
) t
JOIN tma1_model_pricing p
  ON t.model LIKE CONCAT('%', p.model_pattern, '%')
GROUP BY t.model
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

### Latency percentiles (p50 / p95)

```sql
SELECT "span_attributes.gen_ai.request.model" AS model,
       ROUND(APPROX_PERCENTILE_CONT(duration_nano, 0.50) / 1000000.0, 0) AS p50_ms,
       ROUND(APPROX_PERCENTILE_CONT(duration_nano, 0.95) / 1000000.0, 0) AS p95_ms,
       COUNT(*) AS requests
FROM opentelemetry_traces
WHERE "span_attributes.gen_ai.system" IS NOT NULL
  AND timestamp > NOW() - INTERVAL '24 hours'
GROUP BY model
ORDER BY p95_ms DESC
```

### Sessions (from hooks + JSONL transcripts)

The `tma1_messages` table includes token usage columns: `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_creation_tokens`, `duration_ms` (populated for assistant messages from JSONL transcripts). Both `tma1_hook_events` and `tma1_messages` include a `conversation_id` column linking events within the same conversation turn. Agent source is identified by `agent_source` in `tma1_hook_events`: `'claude_code'`, `'codex'`, or `'openclaw'`. OpenClaw session IDs are prefixed `oc:<agentId>:<sessionId>`.

```sql
-- List recent sessions with tool counts
SELECT session_id, conversation_id, agent_source, MIN(ts) AS start_ts, MAX(ts) AS end_ts,
  SUM(CASE WHEN event_type = 'PreToolUse' THEN 1 ELSE 0 END) AS tool_calls,
  SUM(CASE WHEN event_type = 'SubagentStart' THEN 1 ELSE 0 END) AS subagents
FROM tma1_hook_events WHERE ts > NOW() - INTERVAL '24 hours'
GROUP BY session_id, conversation_id, agent_source ORDER BY MIN(ts) DESC

-- Search conversation content
SELECT session_id, conversation_id, ts, message_type, content FROM tma1_messages
WHERE matches_term(content, 'search keyword')
  AND ts > NOW() - INTERVAL '7 days'
ORDER BY ts DESC LIMIT 20

-- Session token usage (from JSONL transcripts)
SELECT session_id, SUM(input_tokens) AS input_tok, SUM(output_tokens) AS output_tok,
  SUM(cache_read_tokens) AS cache_read, SUM(cache_creation_tokens) AS cache_write
FROM tma1_messages WHERE message_type = 'assistant' AND ts > NOW() - INTERVAL '24 hours'
GROUP BY session_id ORDER BY input_tok DESC

-- Session tool breakdown
SELECT tool_name, COUNT(*) AS calls FROM tma1_hook_events
WHERE session_id = '<session_id>' AND event_type = 'PreToolUse'
GROUP BY tool_name ORDER BY calls DESC
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
- "Want to scan for prompt injection attempts?"

Remind the user that the full dashboard is available at http://localhost:14318.
