---
description: Pull recent session content from peer coding agents (Codex, OpenClaw, Copilot CLI) that worked on this project.
argument-hint: "[agent] [count]"
allowed-tools: ["mcp__tma1__get_peer_sessions"]
---

# TMA1 Peer-Agent Lens

You're invoked because the user wants to see what a peer coding agent
left on this project — typically because they ran another agent to
review code or execute a task, and now want you to act on that work
without copy-pasting between terminals.

## Syntax

```
/tma1-peer                 # all peers, latest 1 session each
/tma1-peer codex           # codex, latest 1 session
/tma1-peer codex 3         # codex, latest 3 sessions
/tma1-peer openclaw        # openclaw, latest 1
/tma1-peer copilot 2       # copilot_cli (alias), latest 2
/tma1-peer all 2           # all peers, 2 each
```

## How to handle the invocation

1. **Parse the arguments** after `/tma1-peer`:
   - First token (optional): agent name
   - Second token (optional): count (integer 1-5)
2. **Normalize the agent name**:
   - `codex` → `codex`
   - `openclaw` → `openclaw`
   - `copilot` or `copilot_cli` → `copilot_cli`
   - `all`, `*`, or empty → `""` (means all peers, excludes Claude Code)
   - **Anything else** → reply to the user: `unknown peer agent "<X>"; available: codex, openclaw, copilot, all` and STOP — do not call the tool with an unrecognized name.
3. **Call the MCP tool `mcp__tma1__get_peer_sessions`** with:
   - `agent_source`: parsed agent (or empty string)
   - `limit`: parsed count (default 1, cap 5)
   - `message_limit`: 30
4. **Read the returned conversation messages** and use them as direct input
   for your next reasoning step. **Do not paraphrase** — when acting on
   peer feedback, quote the specific points the peer made so the user can
   verify you got it right.

## What the tool returns

A JSON payload with `sessions` array. Each session has:

- `session_id`, `agent_source`, `started_at`, `last_activity_at`, `duration_minutes`
- `tool_call_count`, `tokens_input`, `tokens_output`, `cwd`
- `messages`: chronological list of user / assistant / thinking messages
- `recent_tool_names`: top 5 tools the peer agent used
- `files_touched`: distinct file paths the peer Read / Edited

Empty `sessions` means no peer activity found in the time window — tell
the user "no recent <agent> sessions on this project" rather than
fabricating context.

## Examples

User: `/tma1-peer codex`
You: (call tool with `agent_source: "codex", limit: 1, message_limit: 30`)
You: "Codex reviewed `auth.go` 12 min ago and left three concrete issues:
     1. ... 2. ... 3. ... Want me to address all three or pick one?"

User: `/tma1-peer`
You: (call tool with `agent_source: "", limit: 1`)
You: "Two peers active recently — Codex (5 min ago, reviewing auth.go) and
     Copilot CLI (20 min ago, deployed staging). Which do you want me
     to dig into?"
