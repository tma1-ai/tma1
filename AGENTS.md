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

Tagline: *"Close the loop around your agent."*

## How it fits together

```
Agent ── OTLP/HTTP ──┐
       ── /api/hooks ┼──► tma1-server (port 14318)
       ── MCP stdio ─┘        │
                              ▼
                        GreptimeDB (port 14000)
                              │
                              ▼
                        Embedded dashboard
```

tma1-server is one Go binary: it manages a child GreptimeDB process, reverse-proxies OTLP, ingests hook events + JSONL transcripts from Claude Code / Codex / Copilot CLI / OpenClaw, runs the perception layer (anomaly detector + injection bundler), and serves the embedded dashboard. The MCP child (`tma1-server mcp-serve`) shares the same GreptimeDB.

**Deep references — read these when you need detail:**
- [`docs/architecture.md`](docs/architecture.md) — module layout, full data flow, per-agent tables, env vars, file index
- [`docs/hooks.md`](docs/hooks.md) — hook injection protocol (5 events × 2 adapters)
- [`docs/mcp-tools.md`](docs/mcp-tools.md) — 7 MCP tools backed by the perception bundler
- [`docs/anomalies.md`](docs/anomalies.md) — anomaly rules, channels, suppression

## Commands

```bash
make build           # Build the binary (sync-plugin first so embedded skill/command tree is fresh)
make build-linux     # Cross-compile for Linux amd64
make build-windows   # Cross-compile for Windows amd64
make run             # Build and run locally
make dev             # Auto-rebuild + restart on .go / .html / .css / .js / .sql change
make install         # Install dev build to $(INSTALL_DIR) — default ~/.tma1/bin — without restarting the running server
make sync-plugin     # Manually mirror claude-plugin/{skills,commands} → server/internal/hooks/{skills,commands} for go:embed
make vet             # go vet on ./cmd/... ./internal/... ./web
make lint            # golangci-lint v2 on the same selector
make lint-js         # ESLint on dashboard JS (requires Node.js)
make test            # Tests with race detector on the same selector
# CI also runs: golangci-lint + ESLint + shellcheck site/public/install.sh + PSScriptAnalyzer on install.ps1
```

`tma1-server` subcommands (default is the long-running server; subcommand mode is single-shot):

```bash
tma1-server                                          # default — long-running HTTP + GreptimeDB process manager
tma1-server mcp-serve                                # JSON-RPC MCP stdio server; spawned per session, talks to the parent's GreptimeDB
tma1-server install   --adapter claude-code|codex [--project DIR] [--skip-project-files] [--dry-run]
tma1-server uninstall --adapter claude-code|codex [--project DIR] [--dry-run] [--purge-data]
tma1-server build [--watch] [--debounce 2s] [--tag NAME] [--project DIR] [--no-color] -- <command> [args...]
tma1-server help [SUB]                               # top-level usage, or `help build` etc. for one subcommand
tma1-server version                                  # print the tma1-server version
```

Every subcommand also accepts `-h` / `--help`. The installer symlinks the
binary as `tma1`, so `tma1 build --help` is equivalent to
`tma1-server build --help`.

See `docs/hooks.md` for the full install / uninstall contract.

## Go conventions

- Strict `handler → service` layering (no ORM, raw HTTP).
- Format with `gofmt` only.
- Imports: three groups — stdlib, external, internal.
- `PascalCase` exported, `camelCase` unexported. Acronyms all-caps: `apiURL`, `greptimeDB`.
- Wrap errors: `fmt.Errorf("context: %w", err)`.
- Graceful shutdown via `context.WithCancel` + `signal.Notify`.
- The `embed.FS` for `web/` lives in `server/web/web.go`.

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
11. **Concurrent MCP dispatch.** `internal/mcp/server.go` spawns one goroutine per `tools/call`, with writes serialised through a single mutex. A slow tool can't wedge stdin or block other replies; `Run` waits on in-flight goroutines before returning so their responses aren't dropped.

On first start, tma1 writes a default GreptimeDB config to `~/.tma1/config/standalone.toml` and launches GreptimeDB with `-c`. That default keeps HTTP, MySQL, and Prometheus Remote Storage enabled, disables Postgres, InfluxDB, OpenTSDB, and Jaeger, and applies conservative local resource limits.

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
(tool history, tokens, current focus, recent files, build state, anomalies).
Use that block when deciding what to do next.

Example shape (values illustrative):

```
<tma1-context>
project: tma1
session: a1b2c3d4
duration: 12 min
tool_calls: 47
tokens: in=84210 out=312045
current_focus: .../internal/perception/peer.go
tools: Bash×18, Edit×12, Read×9, TaskUpdate×4
recent_files: .../perception/peer.go, .../mcp/tools.go, .../hooks/install_cc.go
build: make (running)
build_last_error (6m ago, may have recovered): exit code 1 ...
external_human_changes: 3
external_files: .../path/to/file.go
anomalies:
  - [MEDIUM] human_modified_during_session — Re-read the listed files before assuming your in-memory copy is current.
</tma1-context>
```

Fields are best-effort — most lines only appear when relevant
(`anomalies` / `build_last_error` / `external_*` only render when there's
something worth flagging). `current_focus` reflects your most recent
Edit/Write target.

**You should:**
- Read the <tma1-context> block (when present) before reasoning about the next action
- Trust `external_files` over your in-memory snapshot — re-read those before editing
- Call the MCP tool `get_session_state` if you need a fuller view of your prior tool calls
- Call `get_context_bundle` after compaction or when context feels stale
- Wrap persistent processes (dev servers, watchers like `npm run dev`, `cargo watch`) with `tma1 build --watch -- <cmd>` so output persists past your session; the next agent (or you, after compaction) reads it via `get_build_status`. One-shot commands don't need wrapping — use Bash directly.
<!-- tma1:end -->
