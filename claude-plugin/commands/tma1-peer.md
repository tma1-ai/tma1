---
description: List recent sessions on this project by agent — peers (Codex, OpenClaw, Copilot CLI) or your own.
argument-hint: "[agent] [--full|--summary] [--limit N] [--since 2h]"
allowed-tools: ["mcp__tma1__get_peer_sessions", "mcp__tma1__get_session_transcript"]
---

# TMA1 Peer-Agent Lens — `/tma1-peer`

Full reference lives in `skills/tma1-peer/SKILL.md`; this file carries the
rules for the explicit-invocation path.

## Parse `$ARGUMENTS`

- First token, if it isn't a flag → agent name:
  `codex` · `openclaw` · `copilot` → `copilot_cli` · `claude` / `cc` → `claude_code` ·
  `self` / `me` → yourself · `all` / `*` / absent → `""` (every peer but you).
  Anything else → reply `unknown agent "<X>"; available: codex, openclaw, copilot, claude, self, all` and **STOP**.
- `--summary` → `detail: "summary"` · `--full` → `detail: "full"` · neither → omit (server defaults to preview).
- `--limit N` → `limit` (1-5, default 1; per agent when no agent is named).
- `--since 2h` / `--since 90m` / `--since 30` → `since_min` in minutes (default 1440).
- Legacy positional form `[agent] [limit] [message_limit]` still works; a bare
  leading integer is the limit, not an agent.

## Call the tool

`mcp__tma1__get_peer_sessions` with the parsed values. Pass what the user
gave — don't silently fall back to defaults.

## Use the response

- `sessions[]` — `session_id` / `agent_source` / `last_activity_ago` /
  `duration_minutes` / `tool_call_count` / `messages` / `recent_tool_names` /
  `files_touched` / `cwd`; `is_latest_for_cwd` marks your own current session
  on a `self` lookup.
- `most_recent_session` — lead your answer with it so the user knows whether
  the work is current.
- `partial_failures` — `agent → error`, present only on a failed fan-out.
  Check it before treating empty `sessions` as silence.
- `note` — only when `sessions` is empty and nothing failed.

Rules:

- **Quote the peer's concrete points.** Do not paraphrase.
- List `files_touched` when the next step is "fix what they flagged".
- A message with `truncated: true` was cut — fetch the rest with
  `mcp__tma1__get_session_transcript` for that `session_id`.
- Empty + no failures → `no recent <agent> sessions on this project in the window`, then stop.
- Empty + failures → name the failing peer and quote the error. Don't fabricate.
- Searching by keyword instead of by agent → `/tma1-search`.
