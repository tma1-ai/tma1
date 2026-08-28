---
description: Search past agent sessions on this project by keyword, or read one session by id.
argument-hint: "<keyword> [--agent X] [--days N] [--all-projects] | --session <id>"
allowed-tools: ["mcp__tma1__search_sessions", "mcp__tma1__get_session_transcript"]
---

# TMA1 Session Search — `/tma1-search`

Full reference lives in `skills/tma1-search/SKILL.md`.

## Parse `$ARGUMENTS`

- `--session <id>` → skip search, call `mcp__tma1__get_session_transcript`
  with that id (full id or 6+ character prefix). Never guess an id from a
  bare token — commit hashes and `oc:` / `cp:` ids are indistinguishable
  from one.
- Everything that isn't a flag → `query`.
- `--agent X` → `agent_source` (`codex` · `openclaw` · `copilot` → `copilot_cli` ·
  `claude` / `cc` → `claude_code` · `self`). Omit for every agent.
- `--days N` → `since_min = N × 1440`. Omit for the 7-day default.
- `--all-projects` → `project: "*"`. Omit to stay in the current project.
- No query and no `--session` → ask what to search for. Don't call the tool.

## Then

1. `mcp__tma1__search_sessions` → report matches with their snippets.
2. `mcp__tma1__get_session_transcript` on the chosen `session_id`. If
   `has_more` is true and the answer isn't in the page, call again with
   `next_offset`.
3. Answer by quoting the earlier session, not by summarising it.

Read `match_mode` before presenting results: `substring` means the
indexed word-boundary search (which is case-sensitive) found nothing, so
the query was widened to a case-folded substring scan. Say so.
Empty results carry a `note`; report the silence rather than inventing a
session.
