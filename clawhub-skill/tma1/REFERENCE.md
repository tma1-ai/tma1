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
- `tma1_hook_events` → session events from Claude Code hooks + Codex / Copilot CLI / OpenClaw JSONL parsers (filter via `agent_source`)
- `tma1_messages` → conversation content for all agents (session_id prefixes: `cp:` Copilot CLI, `oc:` OpenClaw; Claude Code and Codex use raw session IDs)
- `tma1_anomaly_emits` → ground-truth log of which anomalies were emitted to which agent session, used by the dashboard's Anomalies tab and the Phase 1.7 validation gates
- `tma1_build_events` → output of `tma1-server build -- <cmd>` (one-shot or `--watch`): stdout / stderr / completion events with exit code, duration, and last error
- `tma1_external_changes` → file system + git activity captured by the git/file sensor (agent vs human attribution)
- `tma1_project_state` → most recent indexed snapshot of each project's structure (language, build / test system, key files, top-level directories)

---

### tma1_hook_events

Per-event log of every Claude Code hook + Codex / Copilot CLI / OpenClaw
JSONL-derived event. 21 columns on a current install (15 from the
base DDL, 2 added by schema migration v1, 4 by v2). `append_mode`.

| Column | Type | Role |
| --- | --- | --- |
| `ts` | TIMESTAMP | TIME INDEX |
| `session_id` | STRING | SKIPPING INDEX |
| `event_type` | STRING | INVERTED INDEX |
| `agent_source` | STRING | INVERTED INDEX (values: `claude_code`, `codex`, `copilot_cli`, `openclaw`) |
| `tool_name` | STRING NULL | raw |
| `tool_input` | STRING NULL | raw JSON, truncated at ~2 KB |
| `tool_result` | STRING NULL | raw, truncated at ~4 KB |
| `tool_use_id`, `agent_id`, `agent_type`, `notification_type` | STRING NULL | raw |
| `"message"` | STRING NULL | quoted — reserved keyword |
| `cwd`, `transcript_path`, `conversation_id` | STRING NULL | raw |
| `permission_mode`, `metadata` | STRING NULL | added by migration v1; `metadata` is a JSON blob for event-specific fields |
| `tool_file_path`, `tool_command_prefix`, `tool_error_summary` | STRING NULL | added by v2 — lifted at ingest from `tool_input` / `tool_result` so anomaly rules can WHERE without re-parsing JSON |
| `tool_success` | BOOLEAN NULL | added by v2 — TRUE/FALSE for PostToolUse / PostToolUseFailure, NULL otherwise |

Most recent 20 events in a session:

```sql
SELECT ts, event_type, tool_name, tool_file_path, tool_command_prefix, tool_success
FROM tma1_hook_events
WHERE session_id = '<sid>'
ORDER BY ts DESC LIMIT 20
```

---

### tma1_messages

Conversation content (user / assistant / thinking) for every agent.
13 columns (8 base + 5 added by migration v1). `append_mode`.
FULLTEXT INDEX on `content` enables `matches_term()` keyword search.

| Column | Type | Role |
| --- | --- | --- |
| `ts` | TIMESTAMP | TIME INDEX |
| `session_id` | STRING | SKIPPING INDEX |
| `message_type` | STRING | INVERTED INDEX |
| `"role"` | STRING | INVERTED INDEX — quoted, reserved keyword (`user` / `assistant` / `thinking` / `tool_use` / `tool_result`) |
| `content` | STRING NULL | FULLTEXT INDEX, bloom backend, English analyzer |
| `model` | STRING NULL | INVERTED INDEX |
| `tool_name`, `tool_use_id` | STRING NULL | links to a `tool_use` / `tool_result` pair |
| `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_creation_tokens` | BIGINT NULL | added by migration v1 |
| `duration_ms` | BIGINT NULL | added by migration v1 |

Token totals per session:

```sql
SELECT SUM(input_tokens)  AS in_tok,
       SUM(output_tokens) AS out_tok,
       SUM(cache_read_tokens) AS cache_read,
       SUM(cache_creation_tokens) AS cache_create
FROM tma1_messages
WHERE session_id = '<sid>'
```

---

### tma1_anomaly_emits

One row per anomaly the Detector emitted to an injection channel.
The dashboard's Anomalies tab and the `/api/anomalies/{budget,follow-rate}`
endpoints read this table — the Detector itself is in-process only.
9 columns, `append_mode`.

| Column | Type | Role |
| --- | --- | --- |
| `ts` | TIMESTAMP | TIME INDEX |
| `session_id` | STRING | SKIPPING INDEX |
| `kind` | STRING | INVERTED INDEX (values listed in SKILL.md "Anomaly rules" table) |
| `severity` | STRING | INVERTED INDEX (`low` / `medium` / `high`) |
| `"channel"` | STRING NULL | quoted — reserved keyword (`user_prompt_submit` / `stop_block` / `post_tool_use`) |
| `evidence` | STRING NULL | human-readable summary of why the rule fired |
| `suggestion` | STRING NULL | concrete next action |
| `related_files` | STRING NULL | JSON-encoded array of file paths |
| `first_emitted_at` | TIMESTAMP NULL | when the rule first fired in this session — stable across re-detections within the 10-min suppression window |

Recent HIGH-severity anomalies for a session:

```sql
SELECT ts, kind, evidence, suggestion, related_files
FROM tma1_anomaly_emits
WHERE session_id = '<sid>' AND severity = 'high'
ORDER BY ts DESC LIMIT 20
```

---

### tma1_build_events

Captured stdout / stderr / completion events from
`tma1-server build [--watch] -- <cmd>`. 13 columns, `append_mode`.
FULLTEXT INDEX on `"message"` enables keyword search on build output.

| Column | Type | Role |
| --- | --- | --- |
| `ts` | TIMESTAMP | TIME INDEX |
| `project` | STRING | SKIPPING INDEX (basename of project root) |
| `command` | STRING NULL | full command line |
| `event_type` | STRING | INVERTED INDEX (`started` / `output` / `completed`) |
| `severity` | STRING NULL | INVERTED INDEX (`info` / `warning` / `error`) |
| `"stream"` | STRING NULL | quoted — `stdout` / `stderr` / `exit` |
| `"message"` | STRING NULL | quoted — FULLTEXT INDEX, bloom backend, English analyzer; line batch (~50 lines per output row) |
| `file_path` | STRING NULL | populated when the line carries a path |
| `line_no` | INT NULL | populated when the line carries a line number |
| `exit_code` | INT NULL | non-NULL only on `completed` events |
| `duration_ms` | BIGINT NULL | non-NULL only on `completed` events |
| `host` | STRING NULL | hostname |
| `"tag"` | STRING NULL | quoted — short label (default: first word of the command) |

Latest completion + last error line per project:

```sql
SELECT project, "tag", exit_code, duration_ms, ts
FROM tma1_build_events
WHERE event_type = 'completed'
  AND ts > now() - INTERVAL '30 minutes'
ORDER BY ts DESC LIMIT 10
```

---

### tma1_external_changes

File system + git activity outside the agent. 8 columns, `append_mode`.

| Column | Type | Role |
| --- | --- | --- |
| `ts` | TIMESTAMP | TIME INDEX |
| `project` | STRING | SKIPPING INDEX |
| `change_type` | STRING | INVERTED INDEX (`file_modified`, `file_added`, `file_deleted`, `git_commit`, `git_branch_switch`) |
| `file_path` | STRING NULL | populated for file-system events |
| `git_sha`, `git_message` | STRING NULL | populated for `git_*` events |
| `attribution` | STRING NULL | INVERTED INDEX (`agent` / `human` / `unknown`) — derived by HookAttributor: agent if a Pre/PostToolUse Edit/Write/MultiEdit/Bash event within ±5 s mentions the same path; otherwise human |
| `host` | STRING NULL | hostname |

Files a human modified in the last 30 min:

```sql
SELECT ts, file_path FROM tma1_external_changes
WHERE project = '<project>'
  AND attribution = 'human'
  AND change_type IN ('file_modified','file_added')
  AND ts > now() - INTERVAL '30 minutes'
ORDER BY ts DESC
```

---

### tma1_project_state

Most recent indexed snapshot of each project's static structure.
One logical row per project, written on first hook event for a
project (and lazily refreshed every 24 h thereafter). 9 columns,
`append_mode` — query the latest row by ORDER BY ts DESC LIMIT 1.

| Column | Type | Role |
| --- | --- | --- |
| `ts` | TIMESTAMP | TIME INDEX |
| `project` | STRING | SKIPPING INDEX |
| `"root"` | STRING NULL | quoted — reserved keyword; absolute path to project root |
| `"language"` | STRING NULL | INVERTED INDEX — quoted, reserved keyword; primary language guess (`go`, `rust`, `python`, `javascript`, `typescript`, `java`, `php`, `ruby`, `elixir`, …) |
| `build_system` | STRING NULL | `go` / `cargo` / `npm` / `pnpm` / `yarn` / `bun` / `make` / `maven` / `gradle` / etc. |
| `test_framework` | STRING NULL | `go test` / `cargo test` / `pytest` / `jest` / `vitest` / `mocha` / `phpunit` / `rspec` / `junit` / `exunit` |
| `frameworks` | STRING NULL | JSON array of additional languages or frameworks detected |
| `key_files` | STRING NULL | JSON array (`README.md`, `CLAUDE.md`, `AGENTS.md`, `LICENSE`, `Makefile`, …) |
| `module_summary` | STRING NULL | JSON object; currently `{ "top_level_dirs": [...] }` capped to 24 entries |

Latest snapshot for a project:

```sql
SELECT ts, "root", "language", build_system, test_framework, key_files
FROM tma1_project_state
WHERE project = '<project>'
ORDER BY ts DESC LIMIT 1
```

(`tma1_schema_version` exists as an internal migration ledger, but
isn't useful for agent queries — leave it alone.)

---

### Sample v2 queries (cross-table)

**Most recent anomalies for the current session, with their suggestion:**

```sql
SELECT ts, kind, severity, "channel", suggestion, related_files
FROM tma1_anomaly_emits
WHERE session_id = '<sid>'
ORDER BY ts DESC LIMIT 20
```

**Build status per project in the last hour:**

```sql
SELECT b.project,
       MAX(b.ts)            AS last_event_at,
       MAX(b.exit_code)     AS last_exit_code,
       MAX(b."message")     AS last_message
FROM tma1_build_events b
WHERE b.ts > now() - INTERVAL '1 hour'
GROUP BY b.project
ORDER BY last_event_at DESC
```

**Files a human modified during the active session
(joins `tma1_hook_events` to find the session window, then
`tma1_external_changes` for human edits in that window):**

```sql
WITH win AS (
  SELECT MIN(ts) AS started_ms, MAX(ts) AS ended_ms, MAX(cwd) AS cwd
  FROM tma1_hook_events WHERE session_id = '<sid>'
)
SELECT ec.ts, ec.file_path
FROM tma1_external_changes ec, win
WHERE ec.attribution = 'human'
  AND ec.change_type IN ('file_modified','file_added')
  AND ec.ts BETWEEN win.started_ms AND win.ended_ms
ORDER BY ec.ts DESC
```

**Action follow-rate (did the agent run a Read on a related file
within 5 tool calls after an anomaly emit?):**

```sql
SELECT a.kind,
       COUNT(*) AS emits,
       SUM(
         CASE WHEN EXISTS (
           SELECT 1
           FROM tma1_hook_events h
           WHERE h.session_id  = a.session_id
             AND h.event_type  = 'PreToolUse'
             AND h.tool_name   = 'Read'
             AND h.ts > a.ts
             AND h.ts < a.ts + INTERVAL '10 minutes'
             AND a.related_files LIKE '%' || h.tool_file_path || '%'
         ) THEN 1 ELSE 0 END
       ) AS followed
FROM tma1_anomaly_emits a
WHERE a.ts > now() - INTERVAL '7 days'
GROUP BY a.kind
```

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

### Copilot CLI Queries (JSONL auto-discovery, no OTel)

Copilot CLI data lives entirely in `tma1_hook_events` (agent_source = `copilot_cli`) and `tma1_messages` (session_id starts with `cp:`).

**Recent sessions with tool / message counts:**
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

**Output tokens by model:**
```sql
SELECT model, SUM(COALESCE(output_tokens, 0)) AS output_tokens, COUNT(*) AS messages
FROM tma1_messages
WHERE session_id LIKE 'cp:%'
  AND model != ''
  AND ts > NOW() - INTERVAL '1 day'
GROUP BY model
ORDER BY output_tokens DESC
```

**Tool call distribution:**
```sql
SELECT tool_name, COUNT(*) AS calls
FROM tma1_hook_events
WHERE agent_source = 'copilot_cli'
  AND event_type = 'PreToolUse'
  AND tool_name != ''
  AND ts > NOW() - INTERVAL '1 day'
GROUP BY tool_name
ORDER BY calls DESC
LIMIT 15
```

**Subagent runs with metadata (model, tokens, duration):**
```sql
SELECT ts, agent_type, metadata
FROM tma1_hook_events
WHERE agent_source = 'copilot_cli'
  AND event_type = 'SubagentStop'
ORDER BY ts DESC
LIMIT 20
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

### Setup / installation

| Symptom | Fix |
| --- | --- |
| `tma1-server` not starting | macOS: check `~/Library/Logs/tma1-server.log`; Linux: `journalctl --user -u tma1-server`; verify port 14318 is free |
| Database not healthy | Wait longer; check port 14000 is free; inspect `~/.tma1/config/standalone.toml` if it was manually reconfigured |
| No data in dashboard | Verify agent OTel config points to TMA1 (Claude Code/OpenClaw: `/v1/otlp`; Codex: separate `/v1/logs`, `/v1/traces`, `/v1/metrics`) and restart the agent |
| Port conflict on 14000 | Set `TMA1_GREPTIMEDB_HTTP_PORT=14001` and update agent endpoint config |
| Dashboard shows "GREPTIMEDB: unreachable" | Database crashed; restart with `tma1-server` |

### Agent loop runtime (v2 — hook injection + MCP)

| Symptom | Fix |
| --- | --- |
| Agent suddenly can't see `mcp__tma1__*` tools / "Connection closed" | MCP stdio child died or stalled. In Claude Code: `/mcp` to reconnect. In Codex: restart the session. Confirm the parent is still up via `pgrep -fl tma1-server`; if it isn't, restart it and the MCP child respawns automatically |
| `<tma1-context>` block not appearing in prompts | `UserPromptSubmit` hook isn't registered. CC: `jq '.hooks.UserPromptSubmit' ~/.claude/settings.json` (expect a non-empty array containing `id: "tma1"`). Codex: `jq '.hooks.UserPromptSubmit' ~/.codex/hooks.json`. If empty, re-run `tma1-server install --adapter claude-code` (or `--adapter codex`) |
| Anomalies exist in dashboard but never reach the agent | Either the hook fires beyond the 300ms injection budget (GreptimeDB is slow — check `~/.tma1/config/standalone.toml` resource limits, the response falls back to empty silently), or the hook script isn't running at all. Run `~/.tma1/hooks/tma1-hook.sh` manually with `{"hook_event_name":"UserPromptSubmit","session_id":"test"}` on stdin; non-empty stdout means the path works |
| `get_external_changes` response contains `partial_error` field | One of two underlying GreptimeDB queries failed; the partial snapshot in `human_changes` / `git_changes` is still valid. Usually transient — retry. Persistent → check DB health |
| Shutdown logs `writeq drain timed out, some background writes may have been dropped` | The 2-second drain budget elapsed before background INSERTs landed. Last few hook events / anomaly emits lost. Acceptable on rare shutdown; recurrent → GreptimeDB under-resourced |
| `.tma1-context.md` not updating | This file callback is **opt-in** (designed for non-MCP agents like Aider / Cursor). Set `TMA1_ENABLE_FILE_CALLBACK=1` on the tma1-server process and restart. If still missing after enabling, verify the project root resolver picked a writable directory: server log shows `tma1-context.md refresh failed` at Debug level on failure |
| Same anomaly injected over and over within one session | InjectionCache (1h TTL) should dedupe by Channel. If this happens it's a bug — open an issue with the anomaly `Kind` and the duplicated `<tma1-context>` blocks attached |

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
