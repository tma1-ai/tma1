---
name: tma1-search
description: "Search past agent sessions on this project by keyword, and read back any session's conversation. Invoke when the answer lives in an earlier session — how something was fixed before, what was decided, what an agent tried last week — or when the user hands you a session id. Trigger phrases: \"what did we decide about\", \"how did we fix this before\", \"search my sessions\", \"find that session\", \"read session <id>\", \"之前是怎么解决的\", \"上次讨论过\", \"找一下以前的 session\", \"/tma1-search\"."
argument-hint: "<keyword> [--agent X] [--days N] [--all-projects] | --session <id>"
allowed-tools: ["mcp__tma1__search_sessions", "mcp__tma1__get_session_transcript"]
---

# TMA1 Session Search

TMA1 keeps every agent session on this machine — yours and other agents' —
in a local database. This skill is the read path: find the session, then
read it.

## Two entry points

```
/tma1-search retry backoff            # keyword search, this project, last 7 days
/tma1-search flaky test --agent codex # only Codex's sessions
/tma1-search migration --days 30      # widen the window
/tma1-search pipeline --all-projects  # every project, not just this one
/tma1-search --session 3d8f1d0a       # read one session directly
```

`--session` takes a full id or a prefix of 6+ characters — the abbreviated
id in the `<tma1-context>` block works. Do **not** infer a session id from
a bare token: commit hashes look the same, and `oc:` / `cp:` ids don't look
like hex at all. Anything that isn't behind `--session` is search text.

## Workflow

1. **Search** — `mcp__tma1__search_sessions` with:
   - `query`: the keyword text (everything that isn't a flag)
   - `agent_source`: `--agent` value if given (`codex` / `openclaw` /
     `copilot` / `claude` / `self`); omit for every agent
   - `since_min`: `--days N` × 1440, default omitted (7 days)
   - `project`: `"*"` when `--all-projects` is given; otherwise omit
2. **Report** the matching sessions with their snippets, so the user can
   pick. One obvious hit and a clear question → go straight to step 3.
3. **Read** — `mcp__tma1__get_session_transcript` with the `session_id`
   from step 1. `has_more: true` means older messages exist; call again
   with the returned `next_offset` if the answer isn't in this page.
4. **Answer with quotes.** The value here is the earlier session's actual
   words, not your summary of them.

## Reading the search response

- `sessions[]` — `session_id`, `agent_source`, `last_activity_ago`,
  `hit_count`, and `snippets[]` (each a window around a match).
- `match_mode` — `term` means whole-word matching, the precise mode.
  `substring` means the word-boundary pass found nothing and the search
  widened; results may include noise, and you should say so.
- `scope_truncated: true` — this project has more sessions in the window
  than the search scanned. Narrow with `--agent` or `--days`.
- `note` — present only when nothing matched. Say so plainly; don't invent
  a plausible past session.

## Search behaviour worth knowing

Word-boundary matching means `Cwd` does not match `peerCwdFilter`. Search
for whole identifiers (`peerCwdFilter`, `get_peer_sessions`) or plain
words. If a query returns nothing, try the fuller identifier before
concluding it never happened.

## Related

- Browsing by agent rather than keyword → `/tma1-peer`.
- Aggregates over the raw tables (cost by model, tool failure rates) →
  the `tma1` skill's `exec_query`.
