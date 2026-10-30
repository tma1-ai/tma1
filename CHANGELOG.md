# Changelog

## v0.3.0-beta3

- **Model pricing no longer disappears.** Pricing rows use a fixed 2024
  timestamp, and the table inherited the database TTL, so compaction
  deleted the whole catalog shortly after seeding. Since beta1 removed the
  Sonnet fallback, every estimated cost showed as unknown.
  `tma1_model_pricing` and `tma1_schema_version` now use `ttl = 'forever'`;
  the catalog reseeds and the cost flow is rebuilt on upgrade.
- **Codex latency works with current Codex.** Traces from
  `codex-app-server` and `codex_exec` are recognized, not only
  `codex_cli_rs`. Latency cards and charts measure `run_sampling_request`
  instead of `stream_request`, which only timed sending the request.
- **install.sh upgrades an existing GreptimeDB.** The official GreptimeDB
  script declined to replace an existing binary under `curl | bash` and
  still exited 0; the installer now downloads into an empty directory and
  checks the new binary's version before swapping it in.

## v0.3.0-beta2

- **Sidebar credits GreptimeDB.** The dashboard sidebar footer now reads
  "Powered by GreptimeDB", linking to the GreptimeDB repository.

## v0.3.0-beta1 — Cross-agent observatory

First beta. The dashboard is rebuilt around comparing agents, and the
numbers it shows are now either measured or explicitly unknown.

- **Redesigned dashboard.** A cross-agent Overview (activity, agent mix,
  recent sessions, tool failures), light/dark themes, responsive layouts,
  and side-by-side session comparison: tokens, cache-read share, tool
  failures, and compaction events.
- **OpenCode support.** `POST /api/import/opencode` accepts the official
  OpenCode v1 session export; `integrations/opencode/tma1.js` imports on
  every `session.idle`. Reimports skip persisted records, including across
  restarts. No hooks or context injection; snapshots over 20 MiB are
  rejected. See `docs/opencode.md`.
- **OpenClaw removed** — its scanner, dashboard view, and skill.
- **Pricing.** 36 built-in model prices, matched by exact model ID or a
  dated snapshot suffix instead of substring. Unknown models, and session
  costs that would need missing cache rates, stay unknown instead of
  falling back to Sonnet rates. Character-count cost estimates are gone.
  The catalog has its own version and reseeds on change, dev builds
  included.
- **Prompt review.** Keyword and session-behaviour scores are replaced by
  an LLM assessment of the request and preceding prompts. Every score
  must quote the source; dimensions without enough context abstain
  instead of scoring zero. The default Anthropic model is
  `claude-sonnet-5`. Saved insights record their rubric version.
- **Advisory anomalies.** Four rules: `stale_file_view`,
  `repeated_failed_build`, `external_modified_during_session`, and
  `context_pressure`. Build failures are grouped by full command and
  directory. No current rule blocks `Stop`; `build_broken_after_my_edit`
  and `test_stuck` no longer emit. See `docs/anomalies.md`.
- **Transcript fixes.** Claude Code streaming usage is recorded as
  per-response deltas rather than once per content block. Messages keep
  their source timestamps instead of ingestion time. Codex records
  `thread_spawn` parents, compaction events, cache-write tokens, and
  input-only responses, and no longer repeats usage from quota updates.
- **Schema migrations 5–6** add `source_record_id` to `tma1_messages` and
  `tma1_hook_events`, and `rubric_version` to `tma1_prompt_insights`.
  Existing telemetry is not rewritten.
- **GreptimeDB pinned to v1.3.0.**
- **Website** rewritten around the new dashboard, with OpenCode and
  OpenTelemetry setup.

## v0.2.0-alpha16 — Database supervision

- **GreptimeDB is supervised.** The child process was started and never
  watched: once it was killed — OOM killer, a stray `pkill`, a crash —
  tma1-server kept running against a dead database until someone restarted
  it by hand. A supervisor goroutine now owns `Wait` on the child and
  respawns it with backoff (1 s, doubling to 30 s) unless a shutdown is
  already underway. A child that stayed up for a minute resets the backoff,
  so a binary that cannot start is retried slowly instead of in a loop.
- **GreptimeDB pinned to v1.2.1**, up from the v1.2.0-beta.2 pre-release.
  The minimum version for `TMA1_GREPTIMEDB_VERSION=latest` moves to v1.2.1
  as well.

## v0.2.0-alpha15 — Local session management

The session data was already all on disk; this release gives the agent a
read path into it.

- **`search_sessions`** — find past sessions by what was said in them,
  across every agent, scoped to the current project by default. Backed by
  the FULLTEXT index on `tma1_messages.content`, with an explicit
  term → substring widening reported as `match_mode` (word-boundary
  matching means `Cwd` does not match `peerCwdFilter`).
- **`get_session_transcript`** — read one session's conversation by id or
  6+ character id prefix (the abbreviation in `<tma1-context>` works),
  paged via `offset` / `has_more`.
- **`exec_query`** — one read-only `SELECT` against the local GreptimeDB,
  returned as columns + rows. The row cap is pushed into SQL; the
  dashboard's `/api/query` is unchanged.
- **`get_peer_sessions` reads your own history** — `agent_source: "self"`
  returns the caller's past sessions instead of erroring. The default
  empty fan-out still excludes the caller. Message volume is now a
  `detail` level (`summary` / `preview` / `full`) rather than a raw
  count; `message_limit` still works and is deprecated.
- **Empty `usage` rows are filtered out** of every session-content path.
  Codex writes one per model call; they were consuming the message
  budget and pushing real conversation out of the window.
- **`/tma1-search` skill + slash command**, and `/tma1-peer` gains flags
  (`--full`, `--summary`, `--limit`, `--since`) alongside the legacy
  positional form.
- **Stale-skill warning at startup.** Upgrading the binary does not
  rewrite the skill files an adapter installed; the server now detects
  that drift and names the fix (`tma1 install --adapter …`) instead of
  silently letting agents read an outdated tool contract.

## v2.0 — Closing the agent loop

v1 made what your agent did visible. v2 makes that visibility actionable
by feeding it back into the agent's reasoning loop.

### Highlights

- **Five Claude Code hooks** registered automatically by
  `tma1-server install --adapter claude-code`: `UserPromptSubmit`
  prepends a session digest before each turn, `SessionStart` orients
  fresh sessions with prior state, `PreCompact` carries critical state
  through context compaction, `PostToolUse` injects per-tool anomaly
  notes, and `Stop` blocks termination on unresolved
  high-severity issues.
- **Seven MCP stdio tools** (ten as of the unreleased section above): `get_context_bundle`, `get_session_state`,
  `get_anomalies`, `get_build_status`, `get_external_changes`,
  `get_project_state`, `get_peer_sessions`. The MCP server is the same
  `tma1-server` binary, invoked via `mcp-serve`.
- **Six anomaly rules** running on every `Detect`, with a 10-minute
  per-session suppression layer + per-rule resolution checks. Each
  anomaly routes through a `Channel` (`user_prompt_submit`,
  `stop_block`) so the same finding never injects twice.
- **`/tma1-peer` slash command** reads what Codex / OpenClaw /
  Copilot CLI just left on this project and brings it into Claude's
  context — no copy-paste between terminals.
- **Three new sensors** populate the perception bundle:
  - Build sensor — wraps build / dev commands and captures output to
    `tma1_build_events` (`tma1-server build [--watch] -- <cmd>`).
  - Git/file sensor — fsnotify + periodic `git log -1` poll, classifies
    changes as `human` or `agent` and writes to `tma1_external_changes`.
  - Project sensor — language / build-system / structural index,
    refreshed daily into `tma1_project_state`.

### One-shot adapter install

```bash
# macOS / Linux
curl -fsSL https://tma1.ai/install.sh | TMA1_ADAPTER=claude-code bash

# Windows
$env:TMA1_ADAPTER = 'claude-code'; irm https://tma1.ai/install.ps1 | iex
```

This wires hook entries into `~/.claude/settings.json`, registers
TMA1 as an MCP server in `~/.claude.json`, drops the `/tma1-peer`
skill into `~/.claude/skills/`, and adds a TMA1 block to your
project's CLAUDE.md (or AGENTS.md). All idempotent — repeat install
only updates what's stale.

### New HTTP endpoints

| Endpoint | Use |
|----------|-----|
| `/api/hooks` | Hook event ingest (request-response, returns injection content) |
| `/api/hooks/stream` | SSE feed of hook events for the live agent canvas |
| `/api/anomalies` | Recent anomalies across sessions |
| `/api/anomalies/budget` | Daily emit count per Kind (1.7 gate ≤ 5 / day) |
| `/api/anomalies/follow-rate` | Did the agent take the suggested action within N tool calls? (1.7 gate ≥ 30%) |

### New GreptimeDB tables

| Table | Source |
|-------|--------|
| `tma1_build_events` | Build sensor (`tma1-server build`) |
| `tma1_external_changes` | Git/file sensor |
| `tma1_project_state` | Project sensor |
| `tma1_anomaly_emits` | Suppression layer logs every emitted anomaly |

The 27 hook event types (`tma1_hook_events`) and conversation table
(`tma1_messages`) from v1 are unchanged. `tool_input` derived columns
(`tool_file_path`, `tool_command_prefix`, `tool_success`,
`tool_error_summary`) are extracted at ingest so anomaly rules don't
have to `regexp_match` the raw blob.

### Configuration additions

| Variable | Default | Effect |
|----------|---------|--------|
| `TMA1_ADAPTER` | (empty) | Wire an agent during install (`claude-code`). Idempotent. |
| `TMA1_DISABLE_INJECTION` | (unset) | When `1`, hook handlers return empty bodies. |
| `TMA1_CONTEXT_PRESSURE_THRESHOLD` | `100000` | Token threshold for `context_pressure` anomaly. |

### Compatibility

Every v1 dashboard view, every v1 endpoint, every v1 table, and the
entire OTel proxy path are unchanged. v2 is additive: if you don't
install an adapter, TMA1 still works as the v1 local-first
observability tool with no behaviour change.

### Documentation

- [docs/mcp-tools.md](docs/mcp-tools.md) — the seven MCP tools
- [docs/hooks.md](docs/hooks.md) — the five hook events, injection
  protocol, fail-safe contract, runtime invariants
- [docs/anomalies.md](docs/anomalies.md) — the six rules, channels,
  suppression, resolution, validation gates
