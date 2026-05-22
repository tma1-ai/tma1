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
| `server/` | Go binary: GreptimeDB process manager + HTTP server + dashboard + v2 agent-loop surface |
| `server/cmd/tma1-server/` | Entry point + embedded FS mount + subcommand routing (`mcp-serve`, `install`, `build`) |
| `server/internal/config/` | Env var config loading + persisted `~/.tma1/settings.json` |
| `server/internal/install/` | Download and verify GreptimeDB binary |
| `server/internal/greptimedb/` | Start, stop, health-check GreptimeDB process + Flow init + versioned schema migrations + per-table DDL (`flows.go` / `anomaly_emits.go` / `build.go` / `external.go` / `project.go`) |
| `server/internal/handler/` | HTTP handlers: /health, /status, /api/query, /api/evaluate, /api/settings, /api/hooks (now returns injection content), /api/hooks/stream (SSE), /api/anomalies{,/budget,/follow-rate}, /v1/otlp/*, dashboard UI |
| `server/internal/hooks/` | Hook script installer + Claude Code adapter (`install --adapter claude-code` writes hook entries, MCP server config, `/tma1-peer` skill + command, project-level instructions block) |
| `server/internal/transcript/` | JSONL transcript watcher (Claude Code) + Codex / OpenClaw / Copilot CLI session log parsers |
| `server/internal/perception/` | v2 perception layer: bundler, anomaly detector (6 rules + channel routing + 10-min suppression + resolvers), incremental injection cache, peer-session reader, file writer for `.tma1-context.md` |
| `server/internal/sensor/build/` | `tma1-server build [--watch] -- <cmd>` subprocess capture: stdout/stderr tee + batched writes into `tma1_build_events`, force-colour env injection |
| `server/internal/sensor/git/` | fsnotify file watcher + 30 s git poll + agent-vs-human attribution honouring `.gitignore` + static ignore list; writes `tma1_external_changes` |
| `server/internal/sensor/project/` | Lazy project-state indexer (language / build / test / key files / top-level dirs) with 24 h TTL gate; writes `tma1_project_state` |
| `server/internal/mcp/` | JSON-RPC 2.0 stdio MCP server (7 tools backed by the perception bundler) — runs as a CC-spawned child via `tma1-server mcp-serve` |
| `server/internal/writeq/` | Bounded write semaphore (max-in-flight cap, drop counter, recovered-panic counter) used by hook ingest + anomaly emit to keep GreptimeDB from being fork-bombed |
| `server/internal/sqlutil/` | Single-source SQL helpers (`Escape`, `EscapeLike`, `Quote`) shared by perception, handler, sensors |
| `server/internal/strutil/` | UTF-8-safe truncation (`SafeTruncate`) used wherever we cap a string before INSERT |
| `server/internal/pathutil/` | Cross-platform path basename / split (handles POSIX + Windows separators for agent-supplied paths) |
| `server/web/` | Embedded dashboard (HTML + JS + CSS via embed.FS), **8** views: Claude Code, Codex, Copilot CLI, OpenClaw, OTel GenAI, Sessions, Prompts, **Anomalies** + Agent Canvas |
| `site/` | Astro landing page → GitHub Pages → tma1.ai |
| `.claude-plugin/` | Claude Code Marketplace registration |
| `claude-plugin/` | Claude Code plugin: `tma1-peer` skill + slash command (source synced into `server/internal/hooks/{skills,commands}/` via `make sync-plugin`, embedded in the binary, dropped to `~/.claude/{skills,commands}/` at install time) |
| `clawhub-skill/tma1/` | SKILL.md + REFERENCE.md — ClawHub-format setup skill (OpenClaw integration), also published to tma1.ai via `site/package.json:prebuild` |
| `docs/` | Long-form references: `hooks.md`, `mcp-tools.md`, `anomalies.md` |

## Architecture

```
Agent (Claude Code / Codex / Copilot CLI / OpenClaw / any GenAI app)
    │  OTLP/HTTP → http://localhost:14318/v1/otlp
    │  Hook events → http://localhost:14318/api/hooks    [request–response;
    │                                                    response body is
    │                                                    injection content for
    │                                                    UserPromptSubmit / Stop /
    │                                                    PostToolUse / SessionStart /
    │                                                    PreCompact]
    │  MCP stdio  ─── tma1-server mcp-serve (child)      [7 tools: get_context_bundle,
    │                                                    get_session_state, get_anomalies,
    │                                                    get_build_status,
    │                                                    get_external_changes,
    │                                                    get_project_state,
    │                                                    get_peer_sessions]
    │  JSONL transcripts → ~/.claude/projects/ (CC) / ~/.codex/sessions/ (Codex) /
    │                      ~/.copilot/session-state/ (Copilot CLI) / ~/.openclaw/agents/ (OpenClaw)
    ▼
tma1-server  port 14318
    │  reverse-proxies OTLP to GreptimeDB
    │  auto-injects x-greptime-pipeline-name for trace requests
    │  receives hook events → tma1_hook_events + SSE broadcast
    │  generates injection content via perception.Bundler + Detector
    │  watches JSONL transcripts → tma1_messages
    │  sensors (started by handler.StartBackgroundTasks):
    │     ├── sensor/git     → tma1_external_changes  (fsnotify + 30 s git poll)
    │     ├── sensor/project → tma1_project_state     (lazy, 24 h TTL)
    │     └── sensor/build   → tma1_build_events      (subprocess via `tma1-server build`)
    │  writeq.Sem caps in-flight inserts (default 64); panics recovered, dropped jobs counted
    ▼
GreptimeDB  (managed by tma1-server)
    │  Flow engine → tma1_*_1m aggregation tables
    │  Versioned schema migrations via tma1_schema_version ledger
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
    │   ├── Anomalies panel inside session detail (reads /api/anomalies?session_id=…)
    │   ├── Replay mode: replay past sessions as agent orchestration animation
    │   └── Live mode: real-time SSE streaming of hook events → canvas visualization
    ├── Prompts view: Prompt evaluation & improvement (heuristic scoring + optional LLM-as-judge)
    │   ├── Overview: score distribution, trend, top suggestions, dimension breakdown
    │   ├── Prompts: card-based list with per-prompt scoring, suggestions, optional LLM deep eval
    │   └── Patterns: verb-based grouping (fix/add/implement/debug/...) with avg score/cost/turns
    └── Anomalies view: cross-session anomaly list, severity filter, 10 s auto-refresh; budget +
        follow-rate validation endpoints back the Phase 1.7 quality gates
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

## Session + v2 agent-loop tables

Created on startup by dedicated `Init*Table()` calls (kept out of `flows.sql`
so they exist before any trace data arrives). All append-only. The
`tma1_schema_version` ledger drives ALTER additions via
`server/internal/greptimedb/schema_migrations.go`.

| Table | Content |
|-------|---------|
| `tma1_hook_events` | All 27 CC hook event types (tool calls, subagent lifecycle, session start/end, compaction, permissions, file changes, tasks, etc.) + Codex / Copilot CLI / OpenClaw JSONL parsing. Base DDL in `flows.go`; v1 migration adds `conversation_id` / `permission_mode` / `metadata` (JSON blob); v2 migration adds ingest-side derived columns `tool_file_path` / `tool_command_prefix` / `tool_success` / `tool_error_summary` so anomaly rules can WHERE without re-parsing JSON. 21 columns total. SKIPPING INDEX on `session_id`, INVERTED INDEX on `event_type` / `agent_source`. |
| `tma1_messages` | Conversation content: user/assistant/thinking messages, tool_use/tool_result. Base DDL in `flows.go`; v1 migration adds `input_tokens` / `output_tokens` / `cache_read_tokens` / `cache_creation_tokens` / `duration_ms`. 13 columns total. FULLTEXT INDEX on `content` (bloom backend, English analyzer) for keyword search via `matches_term()`. |
| `tma1_anomaly_emits` | One row per anomaly the Detector emitted to an injection channel — the ground-truth feed for the Anomalies dashboard view and the `/api/anomalies/{budget,follow-rate}` validation gates. Created by `InitAnomalyEmitsTable`. DDL in `anomaly_emits.go`. 9 columns: `ts`, `session_id`, `kind`, `severity`, `"channel"`, `evidence`, `suggestion`, `related_files` (JSON array), `first_emitted_at`. |
| `tma1_build_events` | stdout/stderr/completion events captured by `tma1-server build [--watch] -- <cmd>`. Created by `InitBuildTable`. DDL in `build.go`. 13 columns including FULLTEXT-indexed `"message"`, `exit_code`, `duration_ms`, `"tag"`. |
| `tma1_external_changes` | File system + git events captured by the git/file sensor; `attribution = agent / human / unknown` set by `HookAttributor` (looks for an Edit/Write/MultiEdit/Bash hook event within ±5 s mentioning the same path). Created by `InitExternalChangesTable`. DDL in `external.go`. 8 columns. |
| `tma1_project_state` | Latest project structure snapshot (language, build/test system, key files, top-level dirs). Created by `InitProjectStateTable`. DDL in `project.go`. 9 columns including reserved-keyword-quoted `"root"` / `"language"`. Lazy refresh, 24 h TTL gate. |
| `tma1_schema_version` | Migration ledger: one row per applied `Migration` (version, description, ts). Created on demand by `RunSchemaMigrations`. Internal — not for agent queries. |

REFERENCE.md (`clawhub-skill/tma1/REFERENCE.md`) carries the formal
column lists + sample queries that get published with the skill.

## Commands

```bash
make build           # Build the binary (sync-plugin first → server/internal/hooks/{skills,commands}/ stays canonical with claude-plugin/)
make build-linux     # Cross-compile for Linux amd64
make build-windows   # Cross-compile for Windows amd64
make run             # Build and run locally
make dev             # Auto-rebuild + restart on .go / .html / .css / .js / .sql change (output piped through scripts/tma1-prettylog when jq is present)
make install         # Install dev build to $(INSTALL_DIR) — default ~/.tma1/bin — without restarting the running server
make sync-plugin     # Manually mirror claude-plugin/{skills,commands} → server/internal/hooks/{skills,commands} for go:embed
make vet             # Run go vet on ./cmd/... ./internal/... ./web
make lint            # Run golangci-lint v2 on the same selector
make lint-js         # Run ESLint on dashboard JS (requires Node.js)
make test            # Run tests with race detector on the same selector
# CI also runs: golangci-lint + ESLint + shellcheck site/public/install.sh + PSScriptAnalyzer on install.ps1
```

`tma1-server` subcommands (the long-running default has none; subcommand mode is single-shot):

```bash
tma1-server                                          # default — long-running HTTP + GreptimeDB process manager
tma1-server mcp-serve                                # JSON-RPC MCP stdio server; spawned by Claude Code per session, talks to the parent's GreptimeDB
tma1-server install --adapter claude-code [--project DIR] [--dry-run]
                                                     # wire hooks + MCP + skill + /tma1-peer + CLAUDE.md block; --dry-run previews without writing
tma1-server build [--watch] [--debounce 2s] [--filter-regex PAT [--filter-invert]] \
                  [--tag NAME] [--project DIR] [--no-color] -- <command> [args...]
                                                     # wrap a subprocess; tee output to terminal + tma1_build_events
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
| `TMA1_QUERY_CONCURRENCY` | `4` | Max concurrent SQL queries from dashboard. Lower (e.g. `2`) if GreptimeDB OOMs on 30d. Range `1`–`32`. Hot-reloadable via `/api/settings`. |
| `TMA1_ADAPTER` | (empty) | **Install-time only** (`install.sh` / `install.ps1`). Set to `claude-code` to run `tma1-server install --adapter claude-code` after the service is healthy. |
| `TMA1_DISABLE_INJECTION` | (unset) | Set to `1` to short-circuit `generateInjection` — `/api/hooks` still records events but returns empty stdout. Escape hatch for dogfooding. |
| `TMA1_ENABLE_FILE_CALLBACK` | (unset) | Set to `1` to refresh `<project_root>/.tma1-context.md` after each hook event. Off by default — MCP / hook injection covers MCP-capable agents; the file is for Aider / Cursor and adds IO + git-sensor self-noise. |
| `TMA1_DEBUG_POSTTOOLUSE` | (unset) | Set to `1` to emit a debug marker on every PostToolUse hook regardless of anomalies. Plumbing aid. |
| `TMA1_CONTEXT_PRESSURE_THRESHOLD` | `100000` | Input-token threshold (whole-session sum) for the R-context-pressure anomaly. ≈ 50 % of CC Sonnet 4's 200 k context. |
| `OPENCLAW_STATE_DIR` | `~/.openclaw` (with `~/.clawdbot` legacy fallback) | Override the OpenClaw session base directory the transcript scanner watches. |

## Key design decisions

1. **No Docker required.** GreptimeDB is downloaded as a static binary into `~/.tma1/bin/`.
2. **No Grafana.** Dashboard is a single HTML file embedded in the Go binary via `embed.FS`.
3. **Thin OTLP proxy.** tma1-server proxies `/v1/otlp/*` to GreptimeDB, auto-injecting required headers for traces. Agents send to one endpoint (port 14318).
4. **No cloud.** All data stays on the user's machine.
5. **No double-writing.** Flow engine derives metrics from traces. Agent sends OTel once.
6. **Wide events.** `trace_id` joins spans + conversations. One click from token spike to full dialogue.
7. **Closing the agent loop (v2).** Observability is one half; the other half is feeding what TMA1 sees back into the agent. Two channels: (a) hook injection — `/api/hooks` is request–response, response body goes straight into the agent's next prompt (or Stop block) for 5 of the 27 registered events; (b) MCP stdio — `tma1-server mcp-serve` exposes 7 perception tools the agent pulls on demand. Anomalies route through `Channel*` constants so the same finding never injects twice. Hook responses are bounded by `hookInjectionTimeout = 300 ms` so a slow GreptimeDB falls back to "no injection" rather than blocking the agent.
8. **Single-writer SQL surface.** All sensors + the hook handler quote literals through `internal/sqlutil` (`Escape`, `EscapeLike`, `Quote`) and truncate through `internal/strutil.SafeTruncate` (rune-safe). Cross-package SQL drift is the #1 way bad UTF-8 reaches GreptimeDB; consolidating here keeps the fix landing once.
9. **Versioned schema migrations.** ALTER TABLE additions live in `internal/greptimedb/schema_migrations.go` as a strict-ascending `[]Migration`, applied after the bare CREATE TABLE init. The `tma1_schema_version` ledger records every applied migration so the next start is idempotent without "swallow duplicate-column errors" heuristics.
10. **Bounded write queue.** `internal/writeq.Sem` caps in-flight background INSERTs against GreptimeDB at 64. Burst paths (subagent storms, replay) can't fork-bomb the process. A panic in any callback is recovered and counted, never crashes the server.

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
| Hook event handler | `server/internal/handler/hooks.go` — flexible map parsing, metadata JSON column, `generateInjection` switch routing to `UserPromptSubmit / Stop / PostToolUse / SessionStart / PreCompact` content paths |
| Anomalies query API | `server/internal/handler/anomalies.go` — `/api/anomalies`, `/api/anomalies/budget`, `/api/anomalies/follow-rate` (reads `tma1_anomaly_emits`, never re-runs the Detector) |
| Hook telemetry | `server/internal/handler/hook_telemetry.go` — periodic per-event call + inject counter, flushed via slog |
| SSE streaming + broadcast | `server/internal/handler/sse.go`, `broadcast.go` |
| Perception bundler | `server/internal/perception/bundle.go` — Bundle + Digest + RenderSummaryDelta; `client.go` is the local SQL HTTP client |
| Anomaly engine | `server/internal/perception/anomaly.go` — 6 rules, channel routing, 10-min suppression, per-rule resolvers, age-evicted history cache |
| Anomaly emit log | `server/internal/perception/anomaly_emits.go` — fire-and-forget INSERTs routed through the handler's `writeq.Sem` |
| Peer-session reader | `server/internal/perception/peer.go` — backs `get_peer_sessions` MCP tool + `/tma1-peer` skill |
| Project-root resolution | `server/internal/perception/file_writer.go` (`ResolveProjectRoot`) — also writes `.tma1-context.md` for non-MCP agents when `TMA1_ENABLE_FILE_CALLBACK=1` |
| Incremental injection cache | `server/internal/perception/injection_cache.go` — per-session digest dedupe so identical context isn't re-emitted every turn |
| MCP stdio server | `server/internal/mcp/server.go` (loop) + `tools.go` (7 ToolHandlers) + `protocol.go` (JSON-RPC + MCP types) |
| Build sensor | `server/internal/sensor/build/capture.go` (Runner / LongRunner) + `store.go` (writes `tma1_build_events`) |
| Git/file sensor | `server/internal/sensor/git/sensor.go` (per-project watcher lifecycle) + `watcher.go` (fsnotify + git poll) + `gitignore.go` + `attribution.go` (agent vs human) + `store.go` |
| Project sensor | `server/internal/sensor/project/sensor.go` (TTL-gated indexer) + `indexer.go` (marker-file heuristics) + `store.go` |
| Schema migrations | `server/internal/greptimedb/schema_migrations.go` — `[]Migration` + ledger DDL |
| Write semaphore | `server/internal/writeq/sem.go` |
| SQL helpers (single source) | `server/internal/sqlutil/sqlutil.go` |
| UTF-8 truncation | `server/internal/strutil/strutil.go` |
| Cross-platform path | `server/internal/pathutil/path.go` |
| CC adapter installer | `server/internal/hooks/install_cc.go` — atomic writes to `~/.claude/settings.json` + `~/.claude.json`, embedded skill/command tree sync, stale-file sweep |
| Hook script installer | `server/internal/hooks/hooks.go` — drops `.sh` / `.ps1` template under `~/.tma1/hooks/` |
| Hook script templates | `server/internal/hooks/tma1-hook.sh.tmpl` (curl -m 0.5) / `tma1-hook.ps1.tmpl` (Invoke-WebRequest -TimeoutSec 1) |
| Transcript watcher (CC JSONL) | `server/internal/transcript/watcher.go` |
| Codex session parser | `server/internal/transcript/codex.go` |
| OpenClaw session parser | `server/internal/transcript/openclaw.go` |
| Copilot CLI session parser | `server/internal/transcript/copilot_cli.go` — `~/.copilot/session-state/`, session rollover on repeated `session.start`, restart-dedup via DB query |
| Dashboard UI | `server/web/index.html` |
| Anomalies view JS | `server/web/js/anomalies.js` — list, severity filter, 10 s auto-refresh, click-to-expand row primitive shared with session detail |
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
| ClawHub skill | `clawhub-skill/tma1/SKILL.md` (+ `REFERENCE.md`) — auto-synced to `site/public/` via `site/package.json:prebuild` |
| v2 long-form docs | `docs/hooks.md`, `docs/mcp-tools.md`, `docs/anomalies.md` |
| Dev-mode log prettifier | `scripts/tma1-prettylog` — jq pipeline for `make dev` JSON output |
| CI workflow | `.github/workflows/ci.yml` |
| Release workflow | `.github/workflows/release.yml` |
| Site deploy workflow | `.github/workflows/deploy-site.yml` |

## Verification

```bash
# 1. Go: vet + test + build (use the explicit selector that CI + Makefile both use)
cd server && go vet ./cmd/... ./internal/... ./web && \
  go test -race -count=1 ./cmd/... ./internal/... ./web && \
  CGO_ENABLED=0 go build -o /dev/null ./cmd/tma1-server

# 2. Full binary build (also runs sync-plugin so the embedded skill/command tree is fresh)
make build   # → server/bin/tma1-server

# 3. Dashboard renders (open in browser, check empty states + Anomalies tab)
open server/web/index.html

# 4. Site: Astro builds (prebuild also syncs clawhub-skill/tma1/{SKILL,REFERENCE}.md into site/public/)
cd site && npm ci && npm run build   # → site/dist/index.html

# 5. Install scripts: lint clean
shellcheck site/public/install.sh
# (PSScriptAnalyzer on install.ps1 in CI)

# 6. End-to-end smoke for the v2 surface (optional, requires a live tma1-server):
tma1-server install --adapter claude-code --dry-run   # preview without writing
curl -s "http://localhost:14318/api/anomalies?limit=10" | jq .
echo '{"hook_event_name":"UserPromptSubmit","session_id":"smoke","cwd":"'"$PWD"'"}' \
  | curl -s -X POST -H 'Content-Type: application/json' --data-binary @- \
    http://localhost:14318/api/hooks    # body = injection content (may be empty)
```

## Explicitly absent (by design)

- No memory / RAG features (separate concern)
- No OTel Collector (direct OTLP to GreptimeDB)
- No authentication (local-only tool)
- No multi-tenant support
- No TypeScript plugin (SKILL.md + shell is sufficient for MVP)

<!-- tma1:start -->
## TMA1 Context Layer

TMA1 thickens the Observe step in your reasoning loop. At the start of each
turn it injects a <tma1-context> block summarising the current session
(tool history, tokens, current focus, recent files). Use that block when
deciding what to do next.

**You should:**
- Read the <tma1-context> block (when present) before reasoning about the next action
- Call the MCP tool `get_session_state` if you need a fuller view of your prior tool calls
- Call `get_context_bundle` after compaction or when context feels stale
<!-- tma1:end -->
