---
name: tma1-search
description: "Search past agent sessions on this project by keyword, and read back any session's conversation. Invoke this skill when the answer lives in an earlier session — how something was fixed before, what was decided, what another agent tried last week — or when the user hands you a session id. Trigger phrases: \"what did we decide about\", \"how did we fix this before\", \"search my sessions\", \"find that session\", \"read session <id>\", \"/tma1-search\"."
---

# TMA1 Session Search (Codex)

TMA1 keeps every agent session on this machine — yours and other agents' —
in a local database. This skill is the read path: find the session, then
read it.

## Invocation

```
/tma1-search retry backoff             # keyword search, this project, last 7 days
/tma1-search flaky test --agent claude # only Claude Code's sessions
/tma1-search migration --days 30       # widen the window
/tma1-search pipeline --all-projects   # every project
/tma1-search --session 3d8f1d0a        # read one session directly
```

`--session` takes a full id or a prefix of 6+ characters. Do **not**
infer a session id from a bare token: commit hashes look identical, and
`oc:` / `cp:` ids don't look like hex at all. Anything not behind
`--session` is search text.

## Workflow

1. **Search** — call the `tma1` MCP server's `search_sessions` tool:
   - `query`: everything that isn't a flag
   - `agent_source`: `--agent` value (`claude` → `claude_code`,
     `openclaw`, `copilot` → `copilot_cli`, `self`); omit for every agent
   - `since_min`: `--days N` × 1440; omit for the 7-day default
   - `project`: `"*"` with `--all-projects`; otherwise omit
2. **Report** the matches with their snippets so the user can pick. One
   obvious hit and a clear question → go straight to step 3.
3. **Read** — `get_session_transcript` with the chosen `session_id`.
   `has_more: true` means older messages exist; call again with the
   returned `next_offset` if the answer isn't in this page.
4. **Answer with quotes** from the earlier session, not a summary.

## Reading the response

- `sessions[]` — `session_id`, `agent_source`, `last_activity_ago`,
  `hit_count`, `snippets[]` (a window around each match).
- `match_mode` — `term` is the indexed pass: whole words, case-sensitive.
  `substring` means that pass found nothing, so the search widened to a
  case-folded substring scan: noisier results, say so.
- `scope_truncated: true` — more sessions exist in the window than were
  scanned; narrow with `--agent` or `--days`.
- `note` — only when nothing matched. Report the silence; don't invent a
  plausible past session.

The indexed pass matches whole words and is case-sensitive: `Cwd` does not
find `peerCwdFilter`, and neither does `peercwdfilter`. Search the
identifier as written. The substring fallback folds case, which is what
rescues a mistyped query.

## Related tools

- Browsing by agent rather than keyword → the `tma1-peer` skill.
- Aggregates over the raw tables (cost by model, tool failure rates) →
  the `tma1` skill's `exec_query`.
