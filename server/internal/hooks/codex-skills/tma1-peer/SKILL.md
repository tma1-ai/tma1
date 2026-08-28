---
name: tma1-peer
description: "List recent sessions on this project by agent — peers (Claude Code, OpenClaw, Copilot CLI) or your own past sessions. Invoke this skill when the user asks you to read another agent's review feedback, see what someone else tried, act on cross-agent context, or recall your own earlier work here. Trigger phrases: \"what did claude do\", \"what did openclaw do\", \"what did copilot do\", \"peer sessions\", \"cross-agent context\", \"what did I do earlier\", \"/tma1-peer\"."
---

# TMA1 Peer-Agent Lens (Codex)

Lists recent sessions on the current project, newest first. Default is
peer agents — the ones that aren't you — because the usual reason to call
this is "Claude Code or Copilot left something here, act on it without me
copy-pasting between terminals". Naming yourself is also allowed.

## Invocation

Auto-selected when the user mentions reading another agent's output on
this project, or typed explicitly:

```
/tma1-peer                          # all peers, latest session each
/tma1-peer claude                   # Claude Code only
/tma1-peer claude --full            # with full conversation content
/tma1-peer self                     # your own recent sessions here
/tma1-peer copilot --limit 2        # latest 2 Copilot CLI sessions
/tma1-peer --summary --limit 5      # headers only, 5 per agent
/tma1-peer claude --since 6h        # widen the time window
```

## Normalize the agent name

| Input             | `agent_source` to send             |
| ----------------- | ---------------------------------- |
| `claude`, `cc`    | `claude_code`                      |
| `claude_code`     | `claude_code`                      |
| `openclaw`        | `openclaw`                         |
| `copilot`         | `copilot_cli`                      |
| `self`, `me`      | `self` (the server resolves it)    |
| `all`, `*`, empty | `""` (every peer except you)       |

Any other value: reply
`unknown agent "<X>"; available: claude, openclaw, copilot, self, all`
and STOP — do not call the tool with an unrecognised name.

## Flags

| Flag | Argument to send | Default |
| --- | --- | --- |
| `--summary` | `detail: "summary"` — header, tools, files; no messages | |
| (none) | `detail: "preview"` — 3 recent messages | default |
| `--full` | `detail: "full"` — 40 recent messages | |
| `--limit N` | `limit` — sessions per agent, `[1, 5]` | 1 |
| `--since 2h` / `--since 90m` | `since_min` in minutes | 1440 |

Legacy positional form `[agent] [limit] [message_limit]` still works; a
bare leading integer is the limit, not an agent.

## Call the tma1 MCP tool

This project's TMA1 install registers an MCP server named `tma1`. Call
its `get_peer_sessions` tool with the parsed arguments. If your runtime
addresses MCP tools by a prefixed name (`tma1.get_peer_sessions`,
`mcp_tma1_get_peer_sessions`), use whichever convention this Codex
install uses — it's the same tool.

## Use the returned content directly

- `sessions` array — identity (`session_id`, `agent_source`, `started_at`,
  `last_activity_at`, `last_activity_ago`, `duration_minutes`, `cwd`),
  volume (`tool_call_count`, `tokens_input`, `tokens_output`), content
  (`messages`, `recent_tool_names`, `files_touched`). On a `self` lookup,
  `is_latest_for_cwd` marks the session you are almost certainly in right
  now — don't report your own current work back as news.
- `most_recent_session` — freshest session's agent + age; lead with it.
- `partial_failures` — `agent → error_message`, present **only** when the
  all-peers fan-out had a failure. **Check it before treating empty
  `sessions` as "no peer activity".**

When you act on what the peer left:

- **Quote concrete points the peer made.** Don't paraphrase — the user
  wants the peer's exact feedback in your context, not your summary.
- **List the files the peer touched**, when the next step is "fix what
  they flagged".
- **Surface the timing.** "Claude left this 12 min ago" beats "recently".
- A message with `truncated: true` was cut to fit; fetch the rest with the
  `get_session_transcript` tool for that `session_id`.

If `sessions` is empty and `partial_failures` is absent, reply
`no recent <agent> sessions on this project in the active window` and
stop — do not fabricate context. If `partial_failures` is present, quote
the failed agent(s) instead of asserting silence.

## Related tools

- Searching by keyword instead of by agent → the `tma1-search` skill
  (`search_sessions`).
- Already holding a `session_id` → `get_session_transcript`.

## Examples

User: "what did claude leave on this project?"
You: (call `get_peer_sessions` with `agent_source: "claude_code", detail: "full"`)
You: "Claude Code reviewed `server/internal/handler/anomalies.go` 8 minutes ago and flagged three concrete issues:
1. fetchEmits has no LIMIT — body cap will truncate before the query finishes.
2. `applyForceColor` env dedup is libc-dependent.
3. anomalyCache.history map never evicts stale sessions.
Want me to address all three or pick one?"

User: `/tma1-peer self`
You: (call `get_peer_sessions` with `agent_source: "self"`)
You: "Two of your earlier sessions here today: 14:20 landed the retry fix,
11:05 was the abandoned migration attempt."
