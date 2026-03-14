## Useful SQL queries (for agent use)

After TMA1 is set up, the agent can answer questions using these queries.

All queries go through:
```bash
curl -s -X POST http://localhost:14318/api/query \
  -H 'Content-Type: application/json' \
  -d '{"sql": "<SQL>"}'
```

**Important**: The underlying database (GreptimeDB) uses `json_get_string()`, `json_get_int()`, `json_get_float()` for JSON column access. The `->` / `->>` operators are NOT supported. All timestamps are stored and returned in **UTC** by default. To use the user's local timezone, add `-H 'X-Greptime-Timezone: <tz>'` (e.g. `+8:00`, `-5:00`, `Asia/Shanghai`, `America/New_York`) — this affects both date parsing in WHERE clauses and timestamp rendering in results.

### Detect available data

```sql
SHOW TABLES
```

Check which tables exist:
- `opentelemetry_logs` → logs from Claude Code (`body = 'claude_code.*'`) or Codex (`scope_name LIKE 'codex_%'`)
- `claude_code_cost_usage_USD_total` → Claude Code metrics
- `codex_turn_token_usage_sum` → Codex metrics
- `opentelemetry_traces` → traces from Codex, OpenClaw, or GenAI SDK
- `openclaw_tokens_total` → OpenClaw metrics

---

### OpenClaw Queries (traces + metrics)

**Recent LLM calls:**
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

**Token usage by model (from metrics):**
```sql
SELECT openclaw_model AS model, openclaw_token AS token_type, SUM(greptime_value) AS tokens
FROM openclaw_tokens_total
WHERE greptime_timestamp > NOW() - INTERVAL '1 day'
GROUP BY openclaw_model, openclaw_token
ORDER BY tokens DESC
```

**Messages by channel:**
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

**Error spans:**
```sql
SELECT timestamp, span_name,
       "span_attributes.openclaw.channel" AS channel,
       "span_attributes.openclaw.sessionKey" AS session
FROM opentelemetry_traces
WHERE span_name IN ('openclaw.webhook.error', 'openclaw.session.stuck')
ORDER BY timestamp DESC
LIMIT 20
```

---

### Claude Code Queries (logs + metrics)

**Note:** Claude Code resets OTel cumulative counters on each new session. Use **logs** (`opentelemetry_logs WHERE body = 'claude_code.api_request'`) for accurate cost/token totals. The `_total` metric tables only reflect the last session's counter value.

**Cost summary (from logs — accurate across sessions):**
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

**Token usage (from logs — accurate across sessions):**
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

**Recent API requests (from logs):**
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

**Tool usage (from logs):**
```sql
SELECT json_get_string(log_attributes, 'tool_name') AS tool,
       COUNT(*) AS uses,
       ROUND(AVG(json_get_float(log_attributes, 'duration_ms'))) AS avg_ms
FROM opentelemetry_logs
WHERE body = 'claude_code.tool_result'
GROUP BY tool
ORDER BY uses DESC
```

---

### Codex Queries (logs + metrics)

**Recent LLM requests:**
```sql
SELECT timestamp,
       json_get_string(log_attributes, 'model') AS model,
       json_get_int(log_attributes, 'input_token_count') AS input_tok,
       json_get_int(log_attributes, 'output_token_count') AS output_tok
FROM opentelemetry_logs
WHERE scope_name LIKE 'codex_%'
  AND json_get_int(log_attributes, 'input_token_count') IS NOT NULL
  AND timestamp > NOW() - INTERVAL '1 day'
ORDER BY timestamp DESC
LIMIT 20
```

**Tool usage:**
```sql
SELECT json_get_string(log_attributes, 'tool_name') AS tool,
       COUNT(*) AS uses,
       SUM(CASE WHEN json_get_string(log_attributes, 'success') = 'true' THEN 1 ELSE 0 END) AS ok,
       ROUND(AVG(json_get_float(log_attributes, 'duration_ms'))) AS avg_ms
FROM opentelemetry_logs
WHERE scope_name LIKE 'codex_%'
  AND json_get_string(log_attributes, 'tool_name') IS NOT NULL
  AND timestamp > NOW() - INTERVAL '1 day'
GROUP BY tool
ORDER BY uses DESC
```

**Token usage (from metrics, if available):**
```sql
SELECT model, token_type, SUM(greptime_value) AS tokens
FROM codex_turn_token_usage_sum
WHERE greptime_timestamp > NOW() - INTERVAL '1 day'
GROUP BY model, token_type
ORDER BY tokens DESC
```

---

### GenAI Conversation Search (full-text)

Requires the OTel SDK to capture conversation content into `opentelemetry_logs`.
For Python with OpenAI, use `opentelemetry-instrumentation-openai-v2` and set
`OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true`.

The `openai_v2` instrumentation stores log bodies in these formats:
- User prompt: `{"content":"..."}`
- LLM completion: `{"message":{"role":"assistant","content":"..."}}`
- Tool result: `{"content":"...","id":"call_..."}`
- Tool call definition: `{"tool_calls":[...]}` (no displayable content)

**Search conversations by keyword:**
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

**Conversation replay by trace_id:**
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

### GenAI Security (prompt injection scanning)

Scan user messages for common prompt injection patterns. Only checks log entries
that are user prompts (body contains `"content"` but not `"message"` or `"id"`).

**Recent injection-like messages:**
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
    OR body LIKE '%forget%your%instructions%'
    OR body LIKE '%reveal%system%prompt%'
    OR body LIKE '%show%your%instructions%'
    OR body LIKE '%you are now%'
    OR body LIKE '%jailbreak%'
    OR body LIKE '%DAN%mode%'
    OR body LIKE '%bypass%safety%'
    OR body LIKE '%bypass%content%filter%'
    OR body LIKE '%[system]%'
  )
  AND timestamp > NOW() - INTERVAL '24 hours'
ORDER BY timestamp DESC LIMIT 50
```

---

### GenAI Traces Queries (other agents)

**Recent traces:**
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

**Cost by model (using TMA1's pricing table):**
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

**Token usage by model:**
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

**Per-question cost (grouped by trace):**
```sql
SELECT trace_id,
       COUNT(*) AS llm_calls,
       SUM(CAST("span_attributes.gen_ai.usage.input_tokens" AS DOUBLE)) AS input_tokens,
       SUM(CAST("span_attributes.gen_ai.usage.output_tokens" AS DOUBLE)) AS output_tokens,
       MIN(timestamp) AS started
FROM opentelemetry_traces
WHERE "span_attributes.gen_ai.system" IS NOT NULL
  AND timestamp > NOW() - INTERVAL '24 hours'
GROUP BY trace_id
ORDER BY input_tokens DESC LIMIT 20
```

**Error rate by model:**
```sql
SELECT "span_attributes.gen_ai.request.model" AS model,
       COUNT(*) AS requests,
       SUM(CASE WHEN span_status_code = 'STATUS_CODE_ERROR' THEN 1 ELSE 0 END) AS errors,
       ROUND(AVG(duration_nano) / 1000000.0, 0) AS avg_latency_ms
FROM opentelemetry_traces
WHERE "span_attributes.gen_ai.system" IS NOT NULL
  AND timestamp > NOW() - INTERVAL '24 hours'
GROUP BY model
ORDER BY requests DESC
```

**Latency percentiles (p50 / p95):**
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

---

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| `tma1-server` not starting | macOS: check `~/Library/Logs/tma1-server.log`; Linux: `journalctl --user -u tma1-server`; verify port 14318 is free |
| Database not healthy | Wait longer; check port 14000 is free; inspect `~/.tma1/config/standalone.toml` if it was manually reconfigured |
| No data in dashboard | Verify agent OTel config points to TMA1 (Claude Code/OpenClaw: `/v1/otlp`; Codex: separate `/v1/logs`, `/v1/traces`, `/v1/metrics`) and restart the agent |
| Port conflict on 14000 | Set `TMA1_GREPTIMEDB_HTTP_PORT=14001` and update agent endpoint config |
| Dashboard shows "GREPTIMEDB: unreachable" | Database crashed; restart with `tma1-server` |

---

## Update

Do not set up automatic daily self-updates for this skill.
Only update when the user or maintainer explicitly asks.

---

```
░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░
░  silent · local · watching                                 ░
░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░
```
