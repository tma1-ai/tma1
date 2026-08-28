# MCP tools

TMA1 exposes ten MCP stdio tools so the agent can pull perception data
on demand. The MCP server is the same `tma1-server` binary, invoked as:

```
tma1-server mcp-serve
```

It speaks JSON-RPC over stdin/stdout. The server is registered in
`~/.claude.json` by `tma1-server install --adapter claude-code`; agents
spawn one MCP process per session.

Which tool to reach for:

| Question | Tool |
|----------|------|
| What is my current state? | `get_context_bundle`, `get_session_state`, `get_anomalies`, `get_build_status`, `get_external_changes`, `get_project_state` |
| Which sessions ran here recently? | `get_peer_sessions` (by agent) |
| Which past session mentioned X? | `search_sessions` (by keyword) |
| What was said in that session? | `get_session_transcript` |
| Anything else | `exec_query` (read-only SELECT) |

`get_peer_sessions` and `search_sessions` return the same session-list
shape; `search_sessions` adds matching snippets. Both hand back a
`session_id` that `get_session_transcript` takes verbatim.

Two operational notes:

- The `mcp-serve` entrypoint redirects all logging to stderr — stdout is
  reserved for JSON-RPC frames. Anything that writes to stdout corrupts
  the protocol; if you fork the server, keep that invariant.
- `mcp-serve` does NOT spawn its own GreptimeDB. It connects to the
  parent `tma1-server` process's database on
  `TMA1_GREPTIMEDB_HTTP_PORT` (default 14000). Make sure `tma1-server`
  is running before spawning MCP sessions.

## get_context_bundle

Aggregate entry point. Returns project name, current session state,
active anomalies (UserPromptSubmit-channel only), build status, recent
external changes, and project structure — the same payload the
`UserPromptSubmit` hook injects.

| Arg | Type | Default | Description |
|-----|------|---------|-------------|
| `session_id` | string | latest for cwd | Override the resolved session |
| `cwd` | string | server cwd | Project root for resolution |

Call this when context feels stale — after a compaction, when you've
switched directories, or as a "what does TMA1 know right now" probe.

## get_session_state

Full state for one session: tool history aggregates, token usage,
current focus, recent files, last build error, external changes
during the session.

| Arg | Type | Default | Description |
|-----|------|---------|-------------|
| `session_id` | string | active session for cwd | Session to inspect |
| `verbose` | boolean | false | When true, include a chronological `actions` array of recent PreToolUse / PostToolUse / PostToolUseFailure entries |
| `action_limit` | integer | 50 | Cap on the verbose action list (clamped to 1-200). Ignored when verbose is false |

The verbose variant is the Phase 0.1 "raw action list" channel (it
folds in what was originally proposed as a separate `get_recent_actions`
tool). Each action carries `ts`, `event_type`, `tool_name`,
`file_path` (when applicable), `command_prefix` (Bash / exec_command),
and `success` (only on PostToolUseFailure — `true` on PostToolUse,
absent on PreToolUse).

## get_anomalies

List anomalies for one session, already routed through suppression so
re-emits within the 10-minute silence window are absent.

| Arg | Type | Default | Description |
|-----|------|---------|-------------|
| `session_id` | string | active session for cwd | Session to inspect |

Each anomaly carries `kind`, `severity`, `channel`, `evidence`,
`suggestion`, `related_files`, `first_emitted_at`. See
[anomalies.md](anomalies.md) for the kinds.

## get_build_status

Most recent build / dev output captured by the build sensor
(`tma1-server build --watch -- <cmd>`).

| Arg | Type | Default | Description |
|-----|------|---------|-------------|
| `tag` | string | most recent | Build watcher tag |

Returns the last error message and timestamp, plus a stale flag when
the last error is older than the build watcher's idle threshold (so
the agent doesn't act on a stale failure).

## get_external_changes

Files modified outside the agent loop, plus git commits and branch
moves, classified as observed `agent` writes or `unknown` source.

| Arg | Type | Default | Description |
|-----|------|---------|-------------|
| `since_min` | integer | 30 | Lookback window in minutes |

Useful when the agent is about to edit something. Combined with
`get_session_state.recent_files`, it answers "did anyone else touch
this file since I last read it?"

## get_project_state

Indexed project structure: language, build system, top-level
directories, key files (README, CLAUDE.md, etc). Refreshed once per
day or on demand via the project sensor.

No arguments. Resolves the project from the calling cwd.

Read this once at the start of a fresh session in a new repo before
running ls/cat/grep — the index already knows the language, build
command, and where the test files live.

## get_peer_sessions

Recent sessions on the same project from peer coding agents (Claude
Code, Codex, OpenClaw, Copilot CLI).

**Caller-aware defaults.** The calling agent is identified by the
`TMA1_MCP_CALLER` env var (set by each adapter's installer:
`claude_code`, `codex`, etc.). An empty `agent_source` fans out over
every agent *except* the caller — Codex sees the other three, Claude
Code sees the other three. Naming an agent explicitly overrides that,
including naming yourself: `agent_source: "self"` (or your own
canonical name) returns your own past sessions on this project.

| Arg | Type | Default | Description |
|-----|------|---------|-------------|
| `agent_source` | string | "" (all peers except caller) | `claude_code` / `codex` / `openclaw` / `copilot_cli` / `self`. Aliases: `cc` / `claude` → `claude_code`, `copilot` → `copilot_cli`, `me` / `mine` → `self` |
| `detail` | string | `preview` | `summary` = no messages; `preview` = 3 recent messages at 240 chars; `full` = 40 recent messages at 1200 chars |
| `limit` | integer | 1 | Sessions per agent, clamped to `[1, 5]`. With empty `agent_source`, applied per peer agent, not globally |
| `since_min` | integer | 1440 | Lookback window in minutes (default 24h) |
| `project` | string | derived from cwd | POSIX absolute (`/foo/bar`) or Windows absolute (`C:\foo`, `C:/foo`, `\\server\share`) = prefix match. Bare name = legacy basename LIKE, matched against either separator style |
| `message_limit` | integer | — | **Deprecated**, use `detail`. Explicit message count `[1, 100]`; overrides `detail` when set. Kept because skill files installed before `detail` existed still send it |

Message content is capped per `detail`; a message that was cut carries
`truncated: true`. Rows GreptimeDB stores with empty content (Codex
writes one synthetic `usage` row per model call) are filtered out so
they can't consume the message budget.

### Response shape

| Field | Type | Notes |
|-------|------|-------|
| `project` | string | Resolved project root used for scoping |
| `agent_filter` | string | Echoes the normalized `agent_source` argument |
| `count` | integer | Length of `sessions` |
| `sessions` | array | Per-session metadata + messages, most-recent first |
| `most_recent_session` | object | Top-level shortcut: `agent_source`, `session_id`, `last_activity_at`, `last_activity_ago` for the freshest session. Present when `count > 0` |
| `sessions[].is_latest_for_cwd` | boolean | Set on a self lookup for the newest session this agent ran in the caller's cwd — almost always the caller's own live session. Not called `is_current`: the MCP child is never told its parent's `session_id`, so two concurrent sessions of one agent in one directory are indistinguishable |
| `partial_failures` | object | `agent → error` map. Present **only** when the all-peers fan-out hit a per-agent SQL error. Consumers must read this before treating empty `sessions` as "no activity" |
| `note` | string | Present only when `sessions` is empty and `partial_failures` is absent — "no peer sessions found for this project in the time window" |

The slash command `/tma1-peer [agent] [count]` is a thin wrapper around
this tool. See `claude-plugin/skills/tma1-peer/SKILL.md` for the full
argument-parsing contract that the skill ships with.

## search_sessions

Finds past sessions by what was said in them. This is the entry point
for "how did we fix this before" / "what did we decide about X".

| Arg | Type | Default | Description |
|-----|------|---------|-------------|
| `query` | string | required | Keyword or phrase |
| `agent_source` | string | "" (every agent, including the caller) | Same values and aliases as `get_peer_sessions` |
| `project` | string | derived from cwd | `"*"` searches every project |
| `since_min` | integer | 10080 (7 days) | Lookback window in minutes |
| `limit` | integer | 5 | Sessions to return, clamped to `[1, 10]` |
| `snippet_limit` | integer | 3 | Matching snippets per session, clamped to `[1, 10]` |

**Scope first, then search.** The project + agent scope is resolved
against `tma1_hook_events` before the keyword search runs against
`tma1_messages`. Searching first and filtering afterwards would let a
busy unrelated project fill the candidate limit and blank out the
current one, and would make the match-mode decision below depend on
other projects' data.

**Two match modes.** `matches_term` (the FULLTEXT index on
`tma1_messages.content`) is word-boundary based and case-insensitive:
`Cwd` does not match `peerCwdFilter`. When the term pass returns nothing
*within the scope*, the search widens to `content LIKE '%query%'` and
reports `match_mode: "substring"` — wider recall, more noise. Consumers
should surface which mode produced the hits.

### Response shape

| Field | Type | Notes |
|-------|------|-------|
| `match_mode` | string | `term` or `substring` (see above) |
| `sessions[]` | array | `session_id`, `agent_source`, `cwd`, `last_activity_at`, `last_activity_ago`, `hit_count`, `snippets[]` |
| `snippets[]` | array | `ts`, `role`, `message_type`, `text` — a ±120 character window centred on the match, cut on rune boundaries |
| `scope_truncated` | boolean | The project had more than 500 sessions in the window; the search covered a subset |
| `note` | string | Present only when nothing matched |

Sessions ingested from JSONL with no hook events keep an empty
`agent_source` rather than being dropped — their content still matched.

## get_session_transcript

Reads one session's conversation. Takes the `session_id` from
`search_sessions` / `get_peer_sessions` / the `<tma1-context>` block.

| Arg | Type | Default | Description |
|-----|------|---------|-------------|
| `session_id` | string | required | Full id, or a prefix of at least 6 characters |
| `message_limit` | integer | 40 | Messages per page, clamped to `[1, 200]` |
| `offset` | integer | 0 | Skip this many of the newest messages |
| `content_chars` | integer | 1200 | Per-message cap, clamped to `[100, 8000]` |
| `message_type` | string | — | Keep only `assistant` / `user` / `thinking` / `tool_use` / `tool_result` |

**Exact id beats prefix.** A full id is looked up as an equality match
first; only if that finds nothing does the prefix `LIKE` run. Without
that ordering, a complete id that happens to be another id's prefix
would be reported as ambiguous. An ambiguous prefix returns
`candidates[]` and a `note` instead of a guess.

**Filter on `message_type`, not `role`.** They are separate columns:
Codex's `tool_use` / `tool_result` / `thinking` live in `message_type`
while `role` stays `assistant` / `user`.

Pagination: `has_more: true` means older messages exist; call again with
the returned `next_offset`. It is derived from one extra fetched row, so
paging costs no additional query.

## exec_query

The escape hatch: one read-only `SELECT` against the local GreptimeDB,
returned as columns + rows.

| Arg | Type | Default | Description |
|-----|------|---------|-------------|
| `sql` | string | required | A single `SELECT` statement |
| `row_limit` | integer | 100 | Clamped to `[1, 1000]` |
| `cell_chars` | integer | 2000 | Per-cell cap, clamped to `[100, 20000]` |

**Accepted surface: one SELECT.** The first keyword (after leading
whitespace and comments) must be `SELECT`, and only one statement is
allowed — one trailing semicolon is tolerated. In GreptimeDB every read
goes through `SELECT`; writes and admin operations are separate
top-level statements, so this is sufficient without a keyword denylist.
`WITH`, `SHOW`, `DESCRIBE`, `EXPLAIN` and `ADMIN` are all rejected;
discover tables with `SELECT ... FROM information_schema.tables`.

The statement is executed as the caller wrote it — comments are skipped
for inspection, never rewritten away — wrapped as
`SELECT * FROM (<stmt>) LIMIT row_limit+1` so the database applies the
cap and the extra row reveals truncation. Deadline is 15 s, well above
the 3 s the perception sensors use, because an agent-authored aggregate
can legitimately be slower than a hook-blocking lookup.

The dashboard's `/api/query` remains an unchecked pass-through: it is
same-origin and driven by TMA1's own JavaScript. The gate exists for the
channel an autonomous agent drives.

| Field | Type | Notes |
|-------|------|-------|
| `columns` / `rows` | array | Result grid |
| `row_count` | integer | Rows returned after the cap |
| `truncated` | boolean | More rows existed than `row_limit` |
| `execution_ms` | integer | Round-trip time |
