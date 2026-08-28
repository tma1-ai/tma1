---
name: tma1-peer
description: "List recent sessions on this project by agent — peer agents (Codex, OpenClaw, Copilot CLI) or your own past sessions. Invoke when the user wants you to read another agent's review feedback, see what someone else tried, act on cross-agent context, or recall your own earlier work here. Trigger phrases: \"what did codex do\", \"what did openclaw do\", \"what did copilot do\", \"peer sessions\", \"cross-agent context\", \"what did I do earlier\", \"我之前在这个项目做了什么\", \"/tma1-peer\"."
argument-hint: "[agent] [--full|--summary] [--limit N] [--since 2h]"
allowed-tools: ["mcp__tma1__get_peer_sessions", "mcp__tma1__get_session_transcript"]
---

# TMA1 Peer-Agent Lens

Lists recent sessions on the current project, newest first. Default is
peer agents — the ones that aren't you — because the usual reason to
call this is "another agent reviewed something, act on it without me
copy-pasting between terminals". Naming yourself is also allowed.

## Syntax

```
/tma1-peer                          # all peers, latest session each
/tma1-peer codex                    # codex only
/tma1-peer codex --full             # codex, with full conversation content
/tma1-peer self                     # your own recent sessions on this project
/tma1-peer codex --limit 3          # codex, latest 3 sessions
/tma1-peer --summary --limit 5      # all peers, headers only, 5 each
/tma1-peer codex --since 6h         # widen the time window
```

Agent names: `codex`, `openclaw`, `copilot` (= `copilot_cli`), `claude`
(= `claude_code`), `self` (= you), `all` / omitted (= every peer but you).
Anything else: reply `unknown agent "<X>"; available: codex, openclaw, copilot, claude, self, all`
and STOP — do not call the tool with an unrecognised name.

## Flags

| Flag | Maps to | Default |
| --- | --- | --- |
| `--summary` | `detail: "summary"` — header, tools, files; no messages | |
| (none) | `detail: "preview"` — 3 recent messages per session | default |
| `--full` | `detail: "full"` — 40 recent messages per session | |
| `--limit N` | `limit` — sessions per agent, 1-5 | 1 |
| `--since 2h` / `--since 90m` | `since_min` in minutes | 1440 (24h) |

Legacy positional form still works: `/tma1-peer codex 3 30` means agent
`codex`, `limit: 3`, `message_limit: 30`. A bare leading integer is the
limit, not an agent (`/tma1-peer 3` → all peers, `limit: 3`). Prefer the
flags above when writing new invocations.

## Call the tool

`mcp__tma1__get_peer_sessions` with the parsed `agent_source`, `detail`,
`limit`, `since_min`. Pass every value the user actually supplied — do
not silently fall back to the defaults.

## Use the response

- `sessions[]` — `session_id`, `agent_source`, `last_activity_ago`,
  `duration_minutes`, `tool_call_count`, `messages`, `recent_tool_names`,
  `files_touched`, `cwd`. On a `self` lookup, `is_latest_for_cwd` marks
  the session that is almost certainly the one you're in right now —
  don't report your own current work back to the user as news.
- `most_recent_session` — freshest session's agent + age. Lead with it so
  the user knows immediately whether the peer work is current.
- `partial_failures` — `agent → error`. Present only when the all-peers
  fan-out had a failure. **Read it before calling empty results silence.**
- `note` — present only when `sessions` is empty and nothing failed.

Rules:

- **Quote what the peer actually said.** Do not paraphrase. Reading the
  peer's exact words is the point of this command.
- Messages are capped per `detail`; a cut message carries `truncated: true`.
  Need the rest? `mcp__tma1__get_session_transcript` with that `session_id`.
- Empty `sessions`, no `partial_failures` → say `no recent <agent> sessions
  on this project in the window` and stop. Don't fabricate.
- Empty `sessions` with `partial_failures` → name the peers that failed and
  quote the error instead of asserting silence.

## Related

- Looking for a session by keyword rather than by agent? Use `/tma1-search`.
- Already have a `session_id`? Go straight to `get_session_transcript`.

## Examples

User: `/tma1-peer codex --full`
You: (call with `agent_source: "codex", detail: "full", limit: 1`)
You: "Codex reviewed `auth.go` 12 min ago and left three concrete issues:
     1. ... 2. ... 3. ... Want me to address all three or pick one?"

User: `/tma1-peer self --limit 3`
You: (call with `agent_source: "self", limit: 3`)
You: "Three of your sessions here today. The current one aside, the 14:20
     session landed the retry fix and the 11:05 one was the failed
     migration attempt."
