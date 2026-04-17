---
title: tma1 — Agent context
---

## What this repo is

TMA1 is local-first LLM agent observability, powered by GreptimeDB.
Three pillars (traces + metrics + logs) unified into wide events — all queryable via SQL.

- **Traces** are the core: GenAI / OpenClaw spans carry model, tokens, latency, status
- **Metrics** are derived from traces via Flow engine (no double-writing), and native OTel metrics are also accepted (auto-creates tables)
- **Logs** carry conversation content (conversation replay)
- **Cross-signal JOIN**: `trace_id` connects spans to conversations

The name comes from TMA-1 (Tycho Magnetic Anomaly-1) in *2001: A Space Odyssey*:
the monolith buried on the moon, silently recording everything until you dig it out.

Tagline: *"Your agent runs. TMA1 remembers."*

## High-level modules

| Path | Role |
|------|------|
| `server/` | Go binary: GreptimeDB process manager + HTTP server + dashboard |
| `server/cmd/tma1-server/` | Entry point + embedded FS mount |
| `server/internal/config/` | Env var config loading |
| `server/internal/install/` | Download and verify GreptimeDB binary |
| `server/internal/greptimedb/` | Start, stop, health-check GreptimeDB process + Flow init |
| `server/internal/handler/` | HTTP handlers: /health, /status, /api/query, /api/evaluate, /api/settings, /api/hooks, /api/hooks/stream (SSE), /v1/otlp/*, dashboard UI |
| `server/internal/hooks/` | Hook script installer for Claude Code integration |
| `server/internal/transcript/` | JSONL transcript watcher (Claude Code) + Codex / OpenClaw / Copilot CLI session log parsers |
| `server/web/` | Embedded dashboard (HTML + JS + CSS via embed.FS), 7 views: Claude Code, Codex, Copilot CLI, OpenClaw, OTel GenAI, Sessions, Prompts + Agent Canvas |
| `site/` | Astro landing page → GitHub Pages → tma1.ai |
| `.claude-plugin/` | Claude Code Marketplace registration |
| `claude-plugin/` | Claude Code plugin: skills for setup + inline queries |
| `clawhub-skill/tma1/` | SKILL.md — ClawHub-format skill (OpenClaw integration) |

## Architecture

```
Agent (Claude Code / Codex / Copilot CLI / OpenClaw / any GenAI app)
    │  OTLP/HTTP → http://localhost:14318/v1/otlp
    │  Hook events → http://localhost:14318/api/hooks (Claude Code)
    │  JSONL transcripts → ~/.claude/projects/ (CC) / ~/.codex/sessions/ (Codex) /
    │                      ~/.copilot/session-state/ (Copilot CLI) / ~/.openclaw/agents/ (OpenClaw)
    ▼
tma1-server  port 14318
    │  reverse-proxies OTLP to GreptimeDB
    │  auto-injects x-greptime-pipeline-name for trace requests
    │  receives hook events → tma1_hook_events + SSE broadcast
    │  watches JSONL transcripts → tma1_messages
    ▼
GreptimeDB  (managed by tma1-server)
    │  Flow engine → tma1_*_1m aggregation tables
    │  HTTP SQL API  port 14000
    ▼
Browser dashboard (served by tma1-server)
    ├── Claude Code view: Overview, Tools, Cost, Anomalies, Traces, Sessions→ (from OTel metrics + logs + traces)
    ├── Codex view: Overview, Tools, Cost, Anomalies, Sessions→ (from OTel logs with scope_name codex_*)
    ├── Copilot CLI view: Overview, Tools, Cost, Sessions→ (from ~/.copilot/session-state/*/events.jsonl)
    ├── OpenClaw view: Overview, Traces, Cost, Search (from openclaw.* trace attrs)
    ├── OTel GenAI view: Overview, Traces, Cost, Security, Search (from gen_ai.* trace attrs)
    ├── Sessions view: Session list, full-screen detail overlay (two-column: Insights + Timeline), file heatmap, agent hierarchy, waterfall, canvas animation
    │   ├── CC/Codex/Copilot CLI "Sessions→" is a link that jumps to Sessions view with agent_source filter
    │   ├── Replay mode: replay past sessions as agent orchestration animation
    │   └── Live mode: real-time SSE streaming of hook events → canvas visualization
    └── Prompts view: Prompt evaluation & improvement (heuristic scoring + optional LLM-as-judge)
        ├── Overview: score distribution, trend, top suggestions, dimension breakdown
        ├── Prompts: card-based list with per-prompt scoring, suggestions, optional LLM deep eval
        └── Patterns: verb-based grouping (fix/add/implement/debug/...) with avg score/cost/turns
```

OTel data goes through tma1-server's OTLP proxy (`/v1/otlp/*`), which forwards to GreptimeDB (port 14000) and auto-injects the `x-greptime-pipeline-name: greptime_trace_v1` header for trace requests. Agents should send OTLP to `http://localhost:14318/v1/otlp`.

Hook events from Claude Code arrive via `POST /api/hooks` (configured as command hooks in `~/.claude/settings.json`, using the auto-installed hook script at `~/.tma1/hooks/tma1-hook.sh` on Unix/macOS or `%USERPROFILE%\.tma1\hooks\tma1-hook.ps1` on Windows). Claude Code's HTTP hook type requires HTTPS, so command hooks with curl are used instead. Codex session logs are auto-discovered from `~/.codex/sessions/` without any hook configuration. Copilot CLI session logs are auto-discovered from `~/.copilot/session-state/` without any hook configuration.

## Data sources

Six data paths, depending on the agent:

**Claude Code** → OTel metrics + logs + traces + hooks + JSONL transcripts:

| Table | Type | Content |
|-------|------|---------|
| `claude_code_token_usage_tokens_total` | Metric (counter) | Tokens by model + type (input/output/cacheRead/cacheCreation) |
| `claude_code_cost_usage_USD_total` | Metric (counter) | Cost in USD by model |
| `claude_code_active_time_seconds_total` | Metric (counter) | Active time by type (cli/user) |
| `claude_code_session_count_total` | Metric (counter) | Session count |
| `claude_code_code_edit_tool_decision_total` | Metric (counter) | Tool decisions by tool/language/decision |
| `claude_code_lines_of_code_count_total` | Metric (counter) | Lines added/removed |
| `opentelemetry_logs` | Log events | api_request, api_error, tool_result, tool_decision, user_prompt |
| `opentelemetry_traces` | Traces | Enhanced telemetry spans (requires `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1`) |

CC trace span types (when enhanced telemetry enabled):

| span_name | Key Attributes | Description |
|-----------|---------------|-------------|
| `claude_code.interaction` | user_prompt_length, interaction.sequence | Root span per user turn (exported on turn end) |
| `claude_code.llm_request` | input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, ttft_ms, speed, attempt | LLM API call |
| `claude_code.tool` | tool_name | Tool call (parent of blocked_on_user + execution) |
| `claude_code.tool.blocked_on_user` | decision (accept/reject), source (config/user_permanent/user_temporary) | Permission wait time |
| `claude_code.tool.execution` | success | Actual tool execution |

Trace attributes are auto-created as columns: `"span_attributes.ttft_ms"`, `"span_attributes.session.id"`, etc. Use double-quoted column names in SQL.

Log attributes are JSON. Use `json_get_string()`, `json_get_int()`, `json_get_float()` to extract fields (GreptimeDB does not support `->` / `->>`). Keys with dots (e.g., `session.id`) are interpreted as nested paths and cannot be extracted.

Additionally, Claude Code hooks (configured in `~/.claude/settings.json` as command hooks) send events to `/api/hooks`, stored in `tma1_hook_events`. All 27 hook event types are supported; event-specific fields are stored in the `metadata` JSON column. The JSONL transcript at `~/.claude/projects/<encoded>/<session>.jsonl` is watched for conversation content, stored in `tma1_messages`.

**Codex** → OTel logs + metrics + JSONL session logs:

| Table | Type | Content |
|-------|------|---------|
| `opentelemetry_logs` | Log events | Requests, tool results, decisions (scope_name LIKE 'codex_%') |
| `codex_turn_token_usage_sum` | Metric (histogram sum) | Token counts by model + type |
| Other `codex_*` tables | Metrics | Various counters/histograms auto-created from OTel metrics |

Codex logs use `scope_name` (not `body`) as the event discriminator. Extract fields via `json_get_string(log_attributes, 'model')`, `json_get_int(log_attributes, 'input_token_count')`, etc.

Additionally, Codex session logs at `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` are auto-discovered and parsed by tma1-server. Tool calls, messages, and subagent hierarchy are extracted and stored in `tma1_hook_events` and `tma1_messages` (agent_source = 'codex'). The parser extracts `conversation_id` from `session_meta.payload.id` (= OTel `conversation.id`), emits `SubagentStop` on `task_complete` events, and captures `user_message` / `agent_message` events into `tma1_messages`. No hook configuration needed.

**Copilot CLI** → JSONL session logs (no OTel):

| Table | Type | Content |
|-------|------|---------|
| `tma1_hook_events` | Synthesized hook events | SessionStart / SessionEnd, PreToolUse / PostToolUse(Failure), SubagentStart / SubagentStop, TaskCompleted, SkillInvoked (agent_source = 'copilot_cli') |
| `tma1_messages` | Conversation content | user / assistant / thinking messages with `output_tokens` (session_id LIKE 'cp:%') |

Copilot CLI session logs at `~/.copilot/session-state/<sessionId>/events.jsonl` are auto-discovered and parsed by tma1-server. Session IDs are stored as `cp:<sessionId>`; when a JSONL file contains multiple logical sessions (Copilot CLI appends across restarts), each `session.start` rolls over the in-memory session ID so they're persisted as distinct DB rows. Parses 11 event types: `session.start`, `session.shutdown`, `session.model_change`, `session.task_complete`, `user.message`, `assistant.message` (content + reasoningText → thinking), `tool.execution_start`, `tool.execution_complete` (success=false → `PostToolUseFailure`), `subagent.started`, `subagent.completed`, `skill.invoked`. Timestamps handle both RFC3339 and Copilot CLI's `MM/DD/YYYY HH:mm:ss` UTC format. No hook configuration needed.

**OpenClaw** → OTel traces + metrics:

| Table | Type | Content |
|-------|------|---------|
| `opentelemetry_traces` | Traces | 5 span types (see below) |
| `openclaw_tokens_total` | Metric (counter) | Token counts by model/channel/provider/token type |
| `openclaw_message_processed_total` | Metric (counter) | Messages processed by channel/outcome |
| `openclaw_message_queued_total` | Metric (counter) | Messages queued by channel/source |
| `openclaw_session_state_total` | Metric (counter) | Session state transitions by state/reason |
| `openclaw_context_tokens_{sum,count,bucket}` | Metric (histogram) | Context window tokens by channel/model (used/limit) |
| `openclaw_run_duration_ms_milliseconds_{sum,count,bucket}` | Metric (histogram) | Run duration by channel |
| `openclaw_queue_depth_{sum,count,bucket}` | Metric (histogram) | Queue depth |
| `openclaw_queue_wait_ms_milliseconds_{sum,count,bucket}` | Metric (histogram) | Queue wait time |
| `openclaw_queue_lane_enqueue_total` | Metric (counter) | Queue lane enqueue events |
| `openclaw_queue_lane_dequeue_total` | Metric (counter) | Queue lane dequeue events |

OpenClaw span types: `openclaw.model.usage` (LLM calls), `openclaw.message.processed` (message handling), `openclaw.webhook.processed` (webhook OK), `openclaw.webhook.error` (webhook error, STATUS_CODE_ERROR), `openclaw.session.stuck` (stuck session, STATUS_CODE_ERROR).

Key trace columns: `span_attributes.openclaw.{model,channel,provider,sessionKey,sessionId,outcome,messageId,tokens.input,tokens.output,tokens.cache_read,tokens.cache_write,tokens.total}`

Additionally, OpenClaw JSONL session transcripts at `~/.openclaw/agents/<agentId>/sessions/<timestamp>_<sessionId>.jsonl` are auto-discovered and parsed by tma1-server (`OPENCLAW_STATE_DIR` env var overrides the base path; legacy `~/.clawdbot/` is also scanned). The JSONL format (pi-coding-agent v3) contains a session header, then tree-structured entries (message, compaction, model_change, etc.). Messages carry full `usage` data (input/output/cacheRead/cacheWrite tokens + cost breakdown). Parsed data is stored in `tma1_hook_events` and `tma1_messages` (agent_source = 'openclaw', session_id prefixed `oc:<agentId>:<sessionId>`). Archive files (`.reset.*`, `.deleted.*`, `.bak.*`) are skipped. No configuration needed.

**Other agents (GenAI SDK)** → OTel traces:

| Table | Type | Content |
|-------|------|---------|
| `opentelemetry_traces` | Traces | GenAI spans with semantic convention attributes |

## Flow aggregations (derived from traces)

4 sink tables derived from `opentelemetry_traces` when trace data is present:

| Sink table | Aggregation |
|------------|-------------|
| `tma1_token_usage_1m` | SUM(input_tokens, output_tokens) per model per minute |
| `tma1_cost_1m` | Estimated cost (tokens × pricing) per model per minute |
| `tma1_latency_1m` | uddsketch for percentile queries per model per minute |
| `tma1_status_1m` | request_count + error_count per model per minute |

Source columns use GenAI semantic conventions:
`span_attributes.gen_ai.request.model`, `span_attributes.gen_ai.usage.input_tokens`, etc.

## Session tables (from hooks + JSONL transcripts)

2 tables for session-level conversation tracking, created by `InitSessionTables()` on startup:

| Table | Content |
|-------|---------|
| `tma1_hook_events` | All 27 CC hook event types (tool calls, subagent lifecycle, session start/end, compaction, permissions, file changes, tasks, etc.) + Codex / Copilot CLI / OpenClaw JSONL parsing. Columns include `conversation_id`, `permission_mode`, `metadata` (JSON blob for event-specific fields). append-only, SKIPPING INDEX on session_id, INVERTED INDEX on event_type/agent_source. |
| `tma1_messages` | Conversation content: user/assistant/thinking messages, tool_use/tool_result (from JSONL transcripts). Columns include `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_creation_tokens` (from CC JSONL assistant message usage). append-only, FULLTEXT INDEX on content for keyword search via `matches_term()`. |

## Commands

```bash
make build           # Build the binary
make build-linux     # Cross-compile for Linux amd64
make build-windows   # Cross-compile for Windows amd64
make run             # Build and run locally
make vet             # Run go vet
make lint            # Run golangci-lint (requires golangci-lint v2)
make lint-js         # Run ESLint on dashboard JS (requires Node.js)
make test            # Run tests with race detector
# CI also runs: golangci-lint + ESLint + shellcheck site/public/install.sh + PSScriptAnalyzer on install.ps1
```

## Go conventions

- Strict `handler → service` layering (no ORM, raw HTTP).
- Format with `gofmt` only.
- Imports: three groups — stdlib, external, internal.
- `PascalCase` exported, `camelCase` unexported. Acronyms all-caps: `apiURL`, `greptimeDB`.
- Wrap errors: `fmt.Errorf("context: %w", err)`.
- Graceful shutdown via `context.WithCancel` + `signal.Notify`.
- The `embed.FS` for `web/` lives in `server/web/web.go`.

## Config (env vars)

| Variable | Default | Description |
|----------|---------|-------------|
| `TMA1_HOST` | `127.0.0.1` | Address tma1-server binds to |
| `TMA1_PORT` | `14318` | HTTP port for tma1-server |
| `TMA1_DATA_DIR` | `~/.tma1` | Directory for GreptimeDB data + binaries |
| `TMA1_GREPTIMEDB_VERSION` | `latest` | GreptimeDB version to download |
| `TMA1_GREPTIMEDB_HTTP_PORT` | `14000` | GreptimeDB HTTP API + OTLP port |
| `TMA1_GREPTIMEDB_GRPC_PORT` | `14001` | GreptimeDB gRPC port |
| `TMA1_GREPTIMEDB_MYSQL_PORT` | `14002` | GreptimeDB MySQL protocol port |
| `TMA1_LOG_LEVEL` | `info` | Log level: debug/info/warn/error |
| `TMA1_DATA_TTL` | `60d` | Default TTL for auto-created tables (2 months) |
| `TMA1_LLM_API_KEY` | (empty) | API key for LLM provider (enables prompt deep evaluation) |
| `TMA1_LLM_PROVIDER` | `anthropic` | LLM provider: `anthropic` or `openai` |
| `TMA1_LLM_MODEL` | (auto) | Model override (default: `claude-sonnet-4-20250514` / `gpt-4o-mini`) |

## Key design decisions

1. **No Docker required.** GreptimeDB is downloaded as a static binary into `~/.tma1/bin/`.
2. **No Grafana.** Dashboard is a single HTML file embedded in the Go binary via `embed.FS`.
3. **Thin OTLP proxy.** tma1-server proxies `/v1/otlp/*` to GreptimeDB, auto-injecting required headers for traces. Agents send to one endpoint (port 14318).
4. **No cloud.** All data stays on the user's machine.
5. **No double-writing.** Flow engine derives metrics from traces. Agent sends OTel once.
6. **Wide events.** `trace_id` joins spans + conversations. One click from token spike to full dialogue.

On first start, tma1 writes a default GreptimeDB config to `~/.tma1/config/standalone.toml` and launches GreptimeDB with `-c`. That default keeps HTTP, MySQL, and Prometheus Remote Storage enabled, disables Postgres, InfluxDB, OpenTSDB, and Jaeger, and applies conservative local resource limits.

## Where to look

| Task | File |
|------|------|
| Entry point / startup | `server/cmd/tma1-server/main.go` |
| Embedded FS mount | `server/cmd/tma1-server/web.go` |
| Config loading | `server/internal/config/config.go` |
| GreptimeDB download | `server/internal/install/install.go` |
| GreptimeDB process mgmt | `server/internal/greptimedb/process.go` |
| Flow SQL (aggregations) | `server/internal/greptimedb/flows.sql` |
| Flow init logic | `server/internal/greptimedb/flows.go` |
| HTTP routes | `server/internal/handler/handler.go` |
| Hook event handler | `server/internal/handler/hooks.go` — flexible map parsing, metadata JSON column |
| SSE streaming + broadcast | `server/internal/handler/sse.go`, `broadcast.go` |
| Hook script installer | `server/internal/hooks/hooks.go` |
| Transcript watcher (CC JSONL) | `server/internal/transcript/watcher.go` |
| Codex session parser | `server/internal/transcript/codex.go` |
| OpenClaw session parser | `server/internal/transcript/openclaw.go` |
| Copilot CLI session parser | `server/internal/transcript/copilot_cli.go` — `~/.copilot/session-state/`, session rollover on repeated `session.start`, restart-dedup via DB query |
| Dashboard UI | `server/web/index.html` |
| Sessions view JS | `server/web/js/sessions.js` — orchestrator (KPI cards, session list, detail loading, search) |
| Sessions sub-modules | `server/web/js/sessions-{stats,detail,insights,waterfall,timeline}.js` — stats computation, detail overlay, insight panels, waterfall chart, timeline rendering |
| Agent Canvas animation | `server/web/js/agent-canvas.js` — canvas animation + tool fade-out + subagent lifecycle + compaction/permission events |
| Prompts view JS | `server/web/js/prompts.js` — heuristic scoring engine, data loading, rendering, LLM eval integration |
| LLM evaluation endpoint | `server/internal/handler/evaluate.go` — `/api/evaluate` (Anthropic/OpenAI proxy for prompt evaluation) |
| Settings endpoint | `server/internal/handler/settings.go` — `GET/POST /api/settings` (read/write server config, hot-reload LLM) |
| Settings persistence | `server/internal/config/settings.go` — Load/save `~/.tma1/settings.json`, env var override logic |
| Codex view JS | `server/web/js/codex.js` |
| Copilot CLI view JS | `server/web/js/copilot-cli.js` (`gcp_*` functions) |
| OpenClaw view JS | `server/web/js/openclaw.js` |
| Embedded FS declaration | `server/web/web.go` |
| Landing page | `site/src/pages/index.astro` |
| Install script (Unix) | `site/public/install.sh` |
| Install script (Windows) | `site/public/install.ps1` |
| ClawHub skill | `clawhub-skill/tma1/SKILL.md` |
| CI workflow | `.github/workflows/ci.yml` |
| Release workflow | `.github/workflows/release.yml` |
| Site deploy workflow | `.github/workflows/deploy-site.yml` |

## Verification

```bash
# 1. Go: vet + test + build
cd server && go vet ./... && go test -race -count=1 ./... && CGO_ENABLED=0 go build -o /dev/null ./cmd/tma1-server

# 2. Full binary build
make build   # → server/bin/tma1-server

# 3. Dashboard renders (open in browser, check empty states)
open server/web/index.html

# 4. Site: Astro builds
cd site && npm ci && npm run build   # → site/dist/index.html

# 5. Install script: shellcheck clean
shellcheck site/public/install.sh
```

## Explicitly absent (by design)

- No memory / RAG features (separate concern)
- No OTel Collector (direct OTLP to GreptimeDB)
- No authentication (local-only tool)
- No multi-tenant support
- No TypeScript plugin (SKILL.md + shell is sufficient for MVP)
