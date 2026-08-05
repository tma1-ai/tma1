# Architecture

Deep reference for the TMA1 codebase. AGENTS.md carries the intent + key
design decisions; this file carries the layout, tables, env vars, and
file index. Read this when you need to know *where* something is or
*what shape* a table has — for *why* it's built this way, see AGENTS.md.

## High-level modules

| Path | Role |
|------|------|
| `server/` | Go binary: GreptimeDB process manager + HTTP server + dashboard + v2 agent-loop surface |
| `server/cmd/tma1-server/` | Entry point + embedded FS mount + subcommand routing (`mcp-serve`, `install`, `uninstall`, `build`) |
| `server/internal/config/` | Env var config loading + persisted `~/.tma1/settings.json` |
| `server/internal/install/` | Download and verify GreptimeDB binary |
| `server/internal/greptimedb/` | Start, stop, health-check GreptimeDB process + Flow init + versioned schema migrations + per-table DDL (`flows.go` / `anomaly_emits.go` / `build.go` / `external.go` / `project.go`) |
| `server/internal/handler/` | HTTP handlers: /health, /status, /api/query, /api/evaluate, /api/settings, /api/hooks (returns injection content), /api/hooks/stream (SSE), /api/anomalies{,/budget,/follow-rate}, /v1/otlp/*, dashboard UI |
| `server/internal/hooks/` | Hook script installer + per-adapter wiring. `install_cc.go` (Claude Code: `~/.claude/settings.json` hooks + `~/.claude.json` MCP + `~/.claude/{skills,commands}/`). `install_codex.go` (Codex: `~/.codex/hooks.json` + `~/.codex/config.toml` `[mcp_servers.tma1]` + `~/.agents/skills/tma1-peer/`, TOML merge via `BurntSushi/toml`). `install_shared.go` carries the helpers both use (atomic writes, JSON-strict reads, owner-prefix-scoped stale sweep, instructions/.gitignore). |
| `server/internal/transcript/` | JSONL transcript watcher (Claude Code) + Codex / OpenClaw / Copilot CLI session log parsers |
| `server/internal/perception/` | v2 perception layer: bundler, anomaly detector (6 rules + channel routing + 10-min suppression + resolvers), incremental injection cache, peer-session reader, file writer for `.tma1-context.md` |
| `server/internal/sensor/build/` | `tma1-server build [--watch] -- <cmd>` subprocess capture: stdout/stderr tee + batched writes into `tma1_build_events`, force-colour env injection |
| `server/internal/sensor/git/` | fsnotify file watcher + 30 s git poll + agent-vs-human attribution honouring `.gitignore` + static ignore list; writes `tma1_external_changes` |
| `server/internal/sensor/project/` | Lazy project-state indexer (language / build / test / key files / top-level dirs) with 24 h TTL gate; writes `tma1_project_state` |
| `server/internal/mcp/` | JSON-RPC 2.0 stdio MCP server (7 tools backed by the perception bundler) — runs as a child of each agent (CC, Codex) via `tma1-server mcp-serve`. Pull-channel tools (`get_anomalies`) use side-effect-free `DetectPreview` so reading state can't silently consume the suppression window and weaken the next Stop block. |
| `server/internal/writeq/` | Bounded write semaphore (max-in-flight cap, drop counter, recovered-panic counter) used by hook ingest + anomaly emit to keep GreptimeDB from being fork-bombed |
| `server/internal/sqlutil/` | Single-source SQL helpers (`Escape`, `EscapeLike`, `Quote`) shared by perception, handler, sensors |
| `server/internal/strutil/` | UTF-8-safe truncation (`SafeTruncate`) used wherever we cap a string before INSERT |
| `server/internal/pathutil/` | Cross-platform path basename / split (handles POSIX + Windows separators for agent-supplied paths) |
| `server/web/` | Embedded dashboard (HTML + JS + CSS via embed.FS), 8 views: Claude Code, Codex, Copilot CLI, OpenClaw, OTel GenAI, Sessions, Prompts, Anomalies + Agent Canvas |
| `site/` | Astro landing page → GitHub Pages → tma1.ai |
| `.claude-plugin/` | Claude Code Marketplace registration |
| `claude-plugin/` | Claude Code plugin: `tma1-peer` skill + slash command (source synced into `server/internal/hooks/{skills,commands}/` via `make sync-plugin`, embedded in the binary, dropped to `~/.claude/{skills,commands}/` at install time) |
| `clawhub-skill/tma1/` | SKILL.md + REFERENCE.md — ClawHub-format setup skill (OpenClaw integration), also published to tma1.ai via `site/package.json:prebuild` |
| `docs/` | Long-form references: this file, `hooks.md`, `mcp-tools.md`, `anomalies.md` |

## Data flow

```
Agent (Claude Code / Codex / Copilot CLI / OpenClaw / any GenAI app)
    │  OTLP/HTTP → http://localhost:14318/v1/otlp
    │  Hook events → http://localhost:14318/api/hooks    [request–response.
    │                                                    CC posts raw; Codex posts
    │                                                    `?envelope=codex` and the
    │                                                    handler wraps the same
    │                                                    injection content in
    │                                                    `hookSpecificOutput.additionalContext`.
    │                                                    Five injection events:
    │                                                    UserPromptSubmit / Stop /
    │                                                    PostToolUse / SessionStart /
    │                                                    PreCompact (PreCompact is
    │                                                    CC-only — Codex's hook
    │                                                    catalogue has no PreCompact).]
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

Six data paths, depending on the agent.

### Claude Code → OTel metrics + logs + traces + hooks + JSONL transcripts

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

### Codex → OTel logs + metrics + JSONL session logs

| Table | Type | Content |
|-------|------|---------|
| `opentelemetry_logs` | Log events | Requests, tool results, decisions (scope_name LIKE 'codex_%') |
| `codex_turn_token_usage_sum` | Metric (histogram sum) | Token counts by model + type |
| Other `codex_*` tables | Metrics | Various counters/histograms auto-created from OTel metrics |

Codex logs use `scope_name` (not `body`) as the event discriminator. Extract fields via `json_get_string(log_attributes, 'model')`, `json_get_int(log_attributes, 'input_token_count')`, etc.

Additionally, Codex session logs at `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` are auto-discovered and parsed by tma1-server. Tool calls, messages, and subagent hierarchy are extracted and stored in `tma1_hook_events` and `tma1_messages` (agent_source = 'codex'). The parser extracts `conversation_id` from `session_meta.payload.id` (= OTel `conversation.id`), emits `SubagentStop` on `task_complete` events, and captures `user_message` / `agent_message` events into `tma1_messages`. No hook configuration needed.

### Copilot CLI → JSONL session logs (no OTel)

| Table | Type | Content |
|-------|------|---------|
| `tma1_hook_events` | Synthesized hook events | SessionStart / SessionEnd, PreToolUse / PostToolUse(Failure), SubagentStart / SubagentStop, TaskCompleted, SkillInvoked (agent_source = 'copilot_cli') |
| `tma1_messages` | Conversation content | user / assistant / thinking messages with `output_tokens` (session_id LIKE 'cp:%') |

Copilot CLI session logs at `~/.copilot/session-state/<sessionId>/events.jsonl` are auto-discovered and parsed by tma1-server. Session IDs are stored as `cp:<sessionId>`; when a JSONL file contains multiple logical sessions (Copilot CLI appends across restarts), each `session.start` rolls over the in-memory session ID so they're persisted as distinct DB rows. Parses 11 event types: `session.start`, `session.shutdown`, `session.model_change`, `session.task_complete`, `user.message`, `assistant.message` (content + reasoningText → thinking), `tool.execution_start`, `tool.execution_complete` (success=false → `PostToolUseFailure`), `subagent.started`, `subagent.completed`, `skill.invoked`. Timestamps handle both RFC3339 and Copilot CLI's `MM/DD/YYYY HH:mm:ss` UTC format. No hook configuration needed.

### OpenClaw → OTel traces + metrics

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

### Other agents (GenAI SDK) → OTel traces

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
| `TMA1_ADAPTER` | (empty) | **Install-time only** (`install.sh` / `install.ps1`). Set to `claude-code`, `codex`, a comma-separated list, or `all` to wire adapters after the service is healthy. Curl-pipe / iex installs pass `--skip-project-files`, so they write only global hooks, MCP entries, and `/tma1-peer` skills. Run `tma1-server install --adapter <name>` from a project root when you also want the AGENTS.md / CLAUDE.md block and `.gitignore` entry. |
| `TMA1_MCP_CALLER` | (empty) | **Install-time only.** Adapter installers write this into each agent's MCP `env` block (`claude_code` / `codex`). The MCP child reads it to drive caller-aware peer-session exclusion so an agent never sees its own sessions on `/tma1-peer`. |
| `TMA1_DISABLE_INJECTION` | (unset) | Set to `1` to short-circuit `generateInjection` — `/api/hooks` still records events but returns empty stdout. Escape hatch for dogfooding. |
| `TMA1_ENABLE_FILE_CALLBACK` | (unset) | Set to `1` to refresh `<project_root>/.tma1-context.md` after each hook event. Off by default — MCP / hook injection covers MCP-capable agents; the file is for Aider / Cursor and adds IO + git-sensor self-noise. |
| `TMA1_DEBUG_POSTTOOLUSE` | (unset) | Set to `1` to emit a debug marker on every PostToolUse hook regardless of anomalies. Plumbing aid. |
| `TMA1_CONTEXT_PRESSURE_THRESHOLD` | `100000` | Input-token threshold (whole-session sum) for the R-context-pressure anomaly. ≈ 50 % of CC Sonnet 4's 200 k context. |
| `OPENCLAW_STATE_DIR` | `~/.openclaw` (with `~/.clawdbot` legacy fallback) | Override the OpenClaw session base directory the transcript scanner watches. |
| `TMA1_RELAY_WAKE_TIMEOUT` | `10m` | How long the relay waits for a woken peer to hand back before nudging the originator's terminal. `0` disables the nudge. Go duration syntax. |
| `TMA1_RELAY_BRACKETED_PASTE` | `1` | iTerm waker wraps multi-line prompts in bracketed-paste markers so the REPL takes them as one block. Set `0` to fall back to single-line injection (collapse newlines) for a REPL that doesn't honour `ESC[?2004h`. |
| `TMA1_RELAY_ITERM_BUSY_GATE` | `1` | iTerm waker skips injection (returns busy, no worker fallback) when the target session `is processing`. Set `0` to inject regardless. |
| `TMA1_OSASCRIPT_PATH` / `TMA1_TMUX_PATH` / `TMA1_CODEX_PATH` / `TMA1_CLAUDE_PATH` | (LookPath) | Override the resolved binary for a waker when launchd's narrow PATH can't find it. |

## Relay handoff (`internal/relay`)

Cross-agent driver↔reviewer auto-handoff. At a milestone the driver/reviewer
calls the `tma1_handoff` MCP tool → `POST /api/relay/signal` (token-gated) →
the `Coordinator` looks up the transition, resolves the counterpart's
registered terminal, and a `Waker` injects the next instruction (with the
peer's summary inline).

- **Wakers**, tried in reliability order (`Registry`): `tmux` (pane-precise,
  `set-buffer` + `paste-buffer -p` bracketed paste + `Enter`) → `iterm`
  (osascript locates the session by its `ITERM_SESSION_ID` UUID, injects via
  `write text … newline no` + a submit Enter; the prompt is passed as
  osascript **argv**, never interpolated into the AppleScript source) →
  `worker` (universal headless fallback: `codex exec` / `claude -p`, detached).
  A busy iTerm session stops the chain (no duplicate worker); a dead/closed
  session id falls through.
- **Terminal registry**: hook scripts report `X-Tma1-Role` +
  `X-Tma1-Terminal` (`tmux=$TMUX_PANE;iterm=$ITERM_SESSION_ID;…`) on every
  event. `SessionStart` registers, `SessionEnd` unregisters, every other
  event (including CC's per-turn `Stop`) refreshes `LastSeen`.
- **Busy-debounce + timeout**: once a role is woken it's marked pending; a
  re-signal to a still-pending role is dropped honestly (caller retries),
  and if the peer never hands back within `TMA1_RELAY_WAKE_TIMEOUT` the
  originator is nudged. Pending clears on the peer's own next signal or
  `SessionEnd`.
- **Configurable transitions**: an optional `~/.tma1/relay.json`
  (`{"transitions":{"plan_ready":{"wake_role":"reviewer","prompt":"…"}}}`)
  overrides the built-in 4-stage table; malformed → built-in defaults.
- **macOS TCC**: the first osascript that controls iTerm triggers an
  Automation-permission prompt. Under launchd (no GUI session) it fails
  silently with `-1743` → the iterm waker surfaces it and falls through.

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
| Perception bundler | `server/internal/perception/bundle.go` — Bundle + Digest + RenderSummaryDelta; `client.go` is the local SQL HTTP client. `Bundler.Caller` (set from `TMA1_MCP_CALLER`) drives peer-session self-exclusion. |
| Anomaly engine | `server/internal/perception/anomaly.go` — 6 rules, channel routing, 10-min suppression, per-rule resolvers, age-evicted history cache. Two public entry points: `Detect` (push-channel — mutates `sessHistory`, INSERTs to `tma1_anomaly_emits`, advances `LastEmittedAt`) and `DetectPreview` (pull-channel — runs the same rules + resolvers but reads suppression state without writing; used by MCP `get_anomalies`) |
| Anomaly emit log | `server/internal/perception/anomaly_emits.go` — fire-and-forget INSERTs routed through the handler's `writeq.Sem` |
| Peer-session reader | `server/internal/perception/peer.go` — backs `get_peer_sessions` MCP tool + `/tma1-peer` skill. Caller-aware exclusion works on BOTH the all-peers fan-out (via `peerAgentList()`) AND the explicit-agent path (rejects requests where `agent_source == TMA1_MCP_CALLER` after normalization). `peerCwdFilter` handles POSIX absolute, Windows absolute (drive-letter + UNC, both separator styles), and bare-name fallback. All-peers fan-out captures per-agent SQL failures and surfaces them via a `partial_failures` map in the MCP payload so callers can distinguish "no sessions" from "1-of-N queries failed". `limit` clamps to `[1, 5]` |
| Project-root resolution | `server/internal/perception/file_writer.go` (`ResolveProjectRoot`) — also writes `.tma1-context.md` for non-MCP agents when `TMA1_ENABLE_FILE_CALLBACK=1` |
| Incremental injection cache | `server/internal/perception/injection_cache.go` — per-session digest dedupe so identical context isn't re-emitted every turn |
| MCP stdio server | `server/internal/mcp/server.go` (concurrent loop, write-mutex serialised) + `tools.go` (7 ToolHandlers) + `protocol.go` (JSON-RPC + MCP types) |
| Build sensor | `server/internal/sensor/build/capture.go` (Runner / LongRunner) + `store.go` (writes `tma1_build_events`) |
| Git/file sensor | `server/internal/sensor/git/sensor.go` (per-project watcher lifecycle) + `watcher.go` (fsnotify + git poll) + `gitignore.go` + `attribution.go` (agent vs human) + `store.go` |
| Project sensor | `server/internal/sensor/project/sensor.go` (TTL-gated indexer) + `indexer.go` (marker-file heuristics) + `store.go` |
| Schema migrations | `server/internal/greptimedb/schema_migrations.go` — `[]Migration` + ledger DDL |
| Write semaphore | `server/internal/writeq/sem.go` |
| SQL helpers (single source) | `server/internal/sqlutil/sqlutil.go` |
| UTF-8 truncation | `server/internal/strutil/strutil.go` |
| Cross-platform path | `server/internal/pathutil/path.go` |
| CC adapter installer | `server/internal/hooks/install_cc.go` — atomic writes to `~/.claude/settings.json` + `~/.claude.json`, embedded skill/command tree sync, owner-prefix-scoped stale sweep so user-installed skills/commands are never deleted |
| Codex adapter installer | `server/internal/hooks/install_codex.go` — atomic writes to `~/.codex/hooks.json` (JSON merge) + `~/.codex/config.toml` (TOML merge via `BurntSushi/toml`), skill drop into `~/.agents/skills/tma1-peer/` |
| Shared install helpers | `server/internal/hooks/install_shared.go` — `installSink` interface + `writeFileAtomic`, `readJSONFileStrict`, `syncEmbeddedTree`, owner-prefix `removeStaleUnder`, `installInstructions`, `installGitignore`, `tma1BinaryPath`, `expandHome` |
| CC adapter uninstaller | `server/internal/hooks/uninstall_cc.go` — reverses install_cc.go; refuse-to-overwrite on malformed JSON, half-marker instructions files surfaced as `UninstallReport.Errors` |
| Codex adapter uninstaller | `server/internal/hooks/uninstall_codex.go` — reverses install_codex.go (TOML merge variant) |
| Shared uninstall helpers | `server/internal/hooks/uninstall_shared.go` — `unregisterTMA1Hooks` (id + legacy command-path predicate), `removeInstructionsBlock` (half-state refusal), `removeMCPServerEntry`, `UninstallReport` shape |
| Codex hook stdin/stdout protocol envelope | `server/internal/handler/hooks.go::wrapInjectionEnvelope` — `?envelope=codex` on `/api/hooks` wraps the four string-content events in `hookSpecificOutput.additionalContext`; Stop passes through verbatim (Codex's block shape `{decision,reason}` matches CC's exactly) |
| Codex live-hook gate | `server/internal/handler/codex_live.go` — in-memory map of Codex sessions actively POSTing hooks; `transcript/codex.go` consults it via `Watcher.IsLiveSession` and skips its own JSONL parse so we don't double-write rows. Keyed on the Codex conversation UUID so hook + JSONL rows align. |
| Hook script installer | `server/internal/hooks/hooks.go` — drops `.sh` / `.ps1` template under `~/.tma1/hooks/` |
| Hook script templates | `server/internal/hooks/tma1-hook.sh.tmpl` (CC, curl -m 0.5) / `tma1-hook.ps1.tmpl` (CC, Invoke-WebRequest -TimeoutSec 1) / `tma1-hook-codex.sh.tmpl` + `tma1-hook-codex.ps1.tmpl` (Codex variants — POST with `?source=codex&envelope=codex`) |
| Transcript watcher (CC JSONL) | `server/internal/transcript/watcher.go` — includes `codexParentSession` map so subagent rollout files attribute to the parent run's conversation UUID, not the filename prefix |
| Codex session parser | `server/internal/transcript/codex.go` — `peekCodexMainUUID` pre-scan + `codexFileContext.effectiveSessionID` resolve to the conversation UUID once `session_meta` is parsed |
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
tma1-server install --adapter codex --dry-run         # same, for Codex CLI
tma1-server uninstall --adapter claude-code --dry-run # preview reverse
curl -s "http://localhost:14318/api/anomalies?limit=10" | jq .
echo '{"hook_event_name":"UserPromptSubmit","session_id":"smoke","cwd":"'"$PWD"'"}' \
  | curl -s -X POST -H 'Content-Type: application/json' --data-binary @- \
    http://localhost:14318/api/hooks    # body = injection content (may be empty)
```
