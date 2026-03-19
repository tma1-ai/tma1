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
| `server/internal/handler/` | HTTP handlers: /health, /status, /api/query, /v1/otlp/*, dashboard UI |
| `server/web/` | Embedded dashboard (HTML + JS + CSS via embed.FS), 4 views: Claude Code, Codex, OpenClaw, OTel GenAI |
| `site/` | Astro landing page → GitHub Pages → tma1.ai |
| `.claude-plugin/` | Claude Code Marketplace registration |
| `claude-plugin/` | Claude Code plugin: skills for setup + inline queries |
| `clawhub-skill/tma1/` | SKILL.md — ClawHub-format skill (OpenClaw integration) |

## Architecture

```
Agent (Claude Code / Codex / OpenClaw / any GenAI app)
    │  OTLP/HTTP → http://localhost:14318/v1/otlp
    ▼
tma1-server  port 14318
    │  reverse-proxies OTLP to GreptimeDB
    │  auto-injects x-greptime-pipeline-name for trace requests
    ▼
GreptimeDB  (managed by tma1-server)
    │  Flow engine → tma1_*_1m aggregation tables
    │  HTTP SQL API  port 14000
    ▼
Browser dashboard (served by tma1-server)
    ├── Claude Code view: Overview, Events, Cost, Search (from OTel metrics + logs)
    ├── Codex view: Overview, Events, Cost, Search (from OTel logs with scope_name codex_*)
    ├── OpenClaw view: Overview, Traces, Cost, Search (from openclaw.* trace attrs)
    └── OTel GenAI view: Overview, Traces, Cost, Security, Search (from gen_ai.* trace attrs)
```

OTel data goes through tma1-server's OTLP proxy (`/v1/otlp/*`), which forwards to GreptimeDB (port 14000) and auto-injects the `x-greptime-pipeline-name: greptime_trace_v1` header for trace requests. Agents should send OTLP to `http://localhost:14318/v1/otlp`.

## Data sources

Four data paths, depending on the agent:

**Claude Code** → OTel metrics + logs (no traces):

| Table | Type | Content |
|-------|------|---------|
| `claude_code_token_usage_tokens_total` | Metric (counter) | Tokens by model + type (input/output/cacheRead/cacheCreation) |
| `claude_code_cost_usage_USD_total` | Metric (counter) | Cost in USD by model |
| `claude_code_active_time_seconds_total` | Metric (counter) | Active time by type (cli/user) |
| `claude_code_session_count_total` | Metric (counter) | Session count |
| `claude_code_code_edit_tool_decision_total` | Metric (counter) | Tool decisions by tool/language/decision |
| `claude_code_lines_of_code_count_total` | Metric (counter) | Lines added/removed |
| `opentelemetry_logs` | Log events | api_request, api_error, tool_result, tool_decision, user_prompt |

Log attributes are JSON. Use `json_get_string()`, `json_get_int()`, `json_get_float()` to extract fields (GreptimeDB does not support `->` / `->>`). Keys with dots (e.g., `session.id`) are interpreted as nested paths and cannot be extracted.

**Codex** → OTel logs + metrics (no traces):

| Table | Type | Content |
|-------|------|---------|
| `opentelemetry_logs` | Log events | Requests, tool results, decisions (scope_name LIKE 'codex_%') |
| `codex_turn_token_usage_sum` | Metric (histogram sum) | Token counts by model + type |
| Other `codex_*` tables | Metrics | Various counters/histograms auto-created from OTel metrics |

Codex logs use `scope_name` (not `body`) as the event discriminator. Extract fields via `json_get_string(log_attributes, 'model')`, `json_get_int(log_attributes, 'input_token_count')`, etc.

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
| Dashboard UI | `server/web/index.html` |
| Codex view JS | `server/web/js/codex.js` |
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
