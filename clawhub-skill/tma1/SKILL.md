---
name: tma1
version: 0.3.0
description: |
  Local-first LLM agent observability that closes the loop:
  records agent work on the user's machine, then feeds the useful
  parts back into the next turn via hooks, MCP tools, and anomaly
  detection.

  Use when users say:
  - "install tma1"
  - "upgrade tma1"
  - "update tma1"
  - "setup observability"
  - "monitor my agent"
  - "how much am I spending on tokens"
  - "what has my agent been doing"
  - "local observability"
  - "I don't want to send data to the cloud"
  - "track token usage"
  - "agent metrics"
  - "agent telemetry"
  - "what is my agent executing"
  - "agent security audit"
  - "prompt injection risk"
  - "close the agent loop"
  - "inject session context"
  - "call tma1 MCP tools"
  - "list anomalies"
  - "show me detected anomalies"
  - "what did Codex / OpenClaw / Copilot do on this project"
  - "/tma1-peer"
  - "block stop on anomalies"
  - "what's the build status"

keywords:
  - tma1
  - observability
  - token usage
  - cost tracking
  - agent monitoring
  - local telemetry
  - otel
  - agent-loop
  - hooks
  - mcp
  - mcp-server
  - anomalies
  - peer-agent
metadata:
  openclaw:
    emoji: "🪨"
---

```
┌──────────────────────────────────────────────────────────────┐
│                                                              │
│   ████████╗███╗   ███╗ █████╗  ██╗                           │
│      ██╔══╝████╗ ████║██╔══██╗ ██║                           │
│      ██║   ██╔████╔██║███████║ ██║                           │
│      ██║   ██║╚██╔╝██║██╔══██║ ██║                           │
│      ██║   ██║ ╚═╝ ██║██║  ██║ ███████╗                      │
│      ╚═╝   ╚═╝     ╚═╝╚═╝  ╚═╝ ╚══════╝                      │
│                                                              │
│   close the loop around your agent.                           │
│                                                              │
└──────────────────────────────────────────────────────────────┘
```

# TMA1

TMA1 gives you local-first observability for your AI agent.
Agent work, token usage, cost, latency — stored locally, queryable with plain SQL.
No cloud account. No Docker. No Grafana setup.
Works with Claude Code, Codex, GitHub Copilot CLI, OpenClaw, or any OTel-enabled agent.

The name comes from TMA-1 (Tycho Magnetic Anomaly-1) in *2001: A Space Odyssey*:
the monolith buried on the moon, silently recording everything until you dig it out.

---

## When to use this skill

Use this skill when the user wants to understand what their AI agent has been doing:

- "how much did my agent spend on tokens today?"
- "show me my agent's activity over the last week"
- "set up local observability for my agent"
- "I don't want to send telemetry to the cloud"
- "what tools is my agent calling?"
- "check for prompt injection attempts"

---

## What you get

TMA1 captures different data depending on the agent:

| Agent | Data path | What it captures |
| --- | --- | --- |
| **Claude Code** | OTel metrics + logs + traces + hooks | Token usage, cost, active time, tool decisions, API requests, TTFT, tool timing, permission waits, user prompts, session conversations |
| **Codex** | OTel logs + metrics + session JSONL | User prompts, LLM calls, tool executions, token usage, session conversations |
| **GitHub Copilot CLI** | Session JSONL (auto-discovered, zero config) | Session lifecycle, user/assistant messages, tool calls + failures, subagent lifecycle, skill invocations, output tokens per response |
| **OpenClaw** | OTel traces + metrics + session JSONL | LLM calls (model, tokens, cache), messages, webhooks, sessions, queue depth, session conversations |
| **Other (GenAI SDK)** | OTel traces + logs | Token usage, cost, latency, conversation replay, prompt injection detection (GenAI semantic conventions) |

The dashboard auto-detects the data source and shows the right view.

All data is stored locally in `~/.tma1/data/` and never leaves your machine.

Dashboard: **http://localhost:14318**

---

## Agent loop (v2)

Beyond passive recording, TMA1 v2 feeds useful context back into the
agent's reasoning loop. Currently wired for **Claude Code** and
**Codex**; the surface is adapter-shaped, so any future agent that
exposes hook + MCP integration points can plug in the same way.

Three channels:

**Hooks** — the install adapter registers every hook event the host
agent supports. Five of those events also pull injection content
from the server, prepended to the agent's next prompt or used as a
Stop block:

| Event | What gets injected | CC | Codex |
| --- | --- | :-: | :-: |
| `UserPromptSubmit` | Per-turn `<tma1-context>` digest — session focus, tokens, recent files, active anomalies (delta-only after the first turn) | ✓ | ✓ |
| `PostToolUse` | Per-tool anomaly notes when a rule explicitly routes to this channel | ✓ | ✓ |
| `SessionStart` | Project orientation + prior-session carry-forward | ✓ | ✓ |
| `PreCompact` | Session digest folded into the compaction summary so it survives context loss | ✓ | — (no equivalent in Codex's hook catalogue) |
| `Stop` | JSON block decision when unresolved HIGH-severity anomalies exist; agent refuses to terminate | ✓ | ✓ |

CC posts the response body raw; Codex posts with `?envelope=codex`
and the same content gets wrapped in
`hookSpecificOutput.additionalContext`. Either way the agent sees
the digest at the right turn boundary.

**MCP stdio tools** — `tma1-server mcp-serve` is registered in each
agent's native MCP config (`~/.claude.json` `mcpServers.tma1` for
CC, `~/.codex/config.toml` `[mcp_servers.tma1]` for Codex). The
agent pulls state on demand:

| Tool | When to call | Returns |
| --- | --- | --- |
| `get_context_bundle` | Top of a turn, or after compaction | Full perception bundle (session + anomalies + build + external + project) |
| `get_session_state` | Recovering action history | Tool calls, tokens, current focus, recent files |
| `get_anomalies` | Before changing approach | Active anomalies, post-suppression |
| `get_build_status` | After suggesting edits | Last exit, errors in last 30 min, latest stderr line |
| `get_external_changes` | After a long break | Human-attributed file edits + git activity |
| `get_project_state` | First time in an unfamiliar repo | Language / build / test / key files / top-level dirs |
| `get_peer_sessions` | User asks "what did Codex / CC / OpenClaw / Copilot just do" or invokes `/tma1-peer` | Recent peer-agent sessions on the same project |

`get_peer_sessions` is caller-aware: the adapter writes
`TMA1_MCP_CALLER` into the MCP `env` block at install time, so a
CC caller's empty-`agent_source` query excludes CC's own sessions
and a Codex caller's excludes Codex's. No accidental self-loops.

**Anomaly rules** — six rules detect agent-loop pathologies; each
routes to a specific channel so the same finding never injects twice:

| Kind | Trigger | Channel |
| --- | --- | --- |
| `stale_file_view` | Agent edits a file a human modified externally after the agent's last Read | `user_prompt_submit` |
| `build_broken_after_my_edit` | Build/test failure naming a just-edited file | `stop_block` when ≥3 failures, else `user_prompt_submit` |
| `repeated_failed_build` | Same Bash prefix failed 3+ times in 30 min | `stop_block` |
| `test_stuck` | Same test-runner prefix failed 3+ times (go test / cargo test / pytest / jest / mocha / rspec / phpunit / mix test) | `user_prompt_submit` |
| `human_modified_during_session` | Human-attributed changes during the active session | `user_prompt_submit` |
| `context_pressure` | Session input tokens cross threshold (default 100k; override via `TMA1_CONTEXT_PRESSURE_THRESHOLD`) | `user_prompt_submit` |

Per-session 10-minute suppression dedupes repeat emits. Resolution
checks auto-clear an anomaly when the agent visibly addresses it
(re-reads a stale file, ships a passing Bash command), so a fix in
turn N stops the warning in turn N+1.

**`/tma1-peer`** — pull a peer agent's recent sessions on this
project into the current agent's context without copy-paste:

- `/tma1-peer codex` — Codex's latest session.
- `/tma1-peer copilot 2` — last 2 Copilot CLI sessions.
- `/tma1-peer` alone — latest session per peer agent (caller
  excluded automatically).

CC gets a native slash command at `~/.claude/commands/tma1-peer.md`
plus a fallback skill at `~/.claude/skills/tma1-peer/SKILL.md`.
Codex gets a skill at `~/.agents/skills/tma1-peer/SKILL.md` —
invoke it the same way you invoke any Codex skill.

**One-shot wiring**:

```
# Claude Code
curl -fsSL https://tma1.ai/install.sh | TMA1_ADAPTER=claude-code bash

# Codex
curl -fsSL https://tma1.ai/install.sh | TMA1_ADAPTER=codex bash
```

Both call `tma1-server install --adapter <name>` after the service
is healthy. Each adapter writes its agent's native config shape
idempotently — re-running only updates what's stale:

| Adapter | Hooks file | MCP config | Skill / command drop | Instructions block |
| --- | --- | --- | --- | --- |
| `claude-code` | `~/.claude/settings.json` | `~/.claude.json` `mcpServers.tma1` | `~/.claude/{skills,commands}/` | `CLAUDE.md` (fallback `AGENTS.md` when `CLAUDE.md` is absent) |
| `codex` | `~/.codex/hooks.json` | `~/.codex/config.toml` `[mcp_servers.tma1]` | `~/.agents/skills/tma1-peer/` | `AGENTS.md` |

Preview the diff before writing: `tma1-server install --adapter <name> --dry-run`.

Reverse with `tma1-server uninstall --adapter <name>` — adapter-
scoped, surgical, leaves user-owned hooks / MCP servers / skills
intact. Add `--purge-data` if you also want to wipe `~/.tma1/data/`
and `~/.tma1/bin/`.

Future adapters plug into the same surface: add a `*Installer`
under `internal/hooks/` plus an `--adapter <name>` case in
`cmd/tma1-server/main.go`, and the rest of the v2 surface (hook
injection, MCP, `/tma1-peer`, instructions block) comes along for
free.

---

## Definition of Done

This task is NOT complete until all of the following are true:

1. TMA1 binary is installed and running (`tma1-server` process)
2. Database engine is healthy at `http://localhost:14000/health`
3. The user's agent is configured to send OTel data to TMA1 (endpoint depends on agent — see Step 3)
4. Agent has been restarted (if applicable)
5. Setup is verified (either `/status` returns ok, or the user confirms data is flowing)
6. The user has been sent the handoff message

---

## Onboarding

### Step 0 — Check for existing installation

`[AGENT]` Check if TMA1 is already running:

```bash
curl -sf http://localhost:14318/health && echo "RUNNING" || echo "NOT_RUNNING"
```

- If `RUNNING` **and** the user asked to **upgrade/update**: continue to Step 1 (the install script handles in-place upgrades — it stops the running server, replaces the binary, and restarts). Then proceed to Step 3 to ensure agent config is up to date.
- If `RUNNING` **and** this is a fresh setup request: skip to Step 3 (configure agent).
- If `NOT_RUNNING`: continue to Step 1.

---

### Step 1 — Install TMA1

`[AGENT]` Download and run the TMA1 installer:

```bash
# macOS / Linux
curl -fsSL https://tma1.ai/install.sh | bash
```

```powershell
# Windows (PowerShell)
irm https://tma1.ai/install.ps1 | iex
```

This will:
1. Download the `tma1-server` binary into `~/.tma1/bin/` (the embedded database is auto-downloaded on first start)
2. Start `tma1-server` (which manages the database engine and serves the dashboard)
3. Print the dashboard URL: `http://localhost:14318`
4. Generate the default database config at `~/.tma1/config/standalone.toml` on first start

If a clean reinstall is needed (wipes all data, config, and logs):

```bash
curl -fsSL https://tma1.ai/install.sh | TMA1_FORCE=1 bash
```

If you also want the **agent loop** wired up (hooks + MCP server +
`/tma1-peer`) in a single shot, pass the adapter for your host
agent:

```bash
# Claude Code
curl -fsSL https://tma1.ai/install.sh | TMA1_ADAPTER=claude-code bash

# Codex
curl -fsSL https://tma1.ai/install.sh | TMA1_ADAPTER=codex bash
```

Either form calls `tma1-server install --adapter <name>` after the
service is healthy. The adapter writes the agent's native config
(see the table in the **Agent loop (v2)** section above for the
exact files touched), drops the `tma1-peer` skill / command, and
adds a `<!-- tma1:start -->` block to the project's instructions
file. Idempotent — repeat runs only update what's stale. When this
path runs, the manual hook edits in Step 5 are no longer necessary
for that agent; verification still applies.

Preview before writing: `tma1-server install --adapter <name> --dry-run`.

Wait ~15 seconds for the database to start, then verify:

```bash
curl -sf http://localhost:14318/health && echo "OK" || echo "FAILED"
```

If it fails, tell the user:
> TMA1 didn't start correctly. Check logs for errors: on macOS `~/Library/Logs/tma1-server.log`, on Linux `journalctl --user -u tma1-server`, on Windows check Task Scheduler history for the "TMA1 Server" task.

---

### Step 2 — Verify database is healthy

```bash
curl -sf http://localhost:14000/health && echo "DB OK" || echo "DB NOT READY"
```

If not healthy after 30 seconds, something is wrong with the install. Ask the user to check logs.

---

### Step 3 — Configure the agent

`[AGENT]` Configure the user's agent to send telemetry to TMA1. Choose the section that matches:

#### OpenClaw

First install and enable the diagnostics-otel plugin, then configure and restart:

```bash
openclaw plugins install @openclaw/diagnostics-otel
openclaw plugins enable diagnostics-otel
openclaw config set diagnostics.enabled true
openclaw config set diagnostics.otel.enabled true
openclaw config set diagnostics.otel.endpoint http://localhost:14318/v1/otlp
openclaw config set diagnostics.otel.serviceName openclaw-gateway
openclaw config set diagnostics.otel.traces true
openclaw config set diagnostics.otel.metrics true
openclaw config set diagnostics.otel.logs true
openclaw gateway restart
```

Session transcripts at `~/.openclaw/agents/*/sessions/*.jsonl` are auto-discovered — no extra configuration needed for Sessions and Prompts views.

#### Claude Code

Merge into `~/.claude/settings.json` (Windows: `%USERPROFILE%\.claude\settings.json`):

> **CRITICAL: You MUST read the existing `settings.json` first and MERGE — NEVER overwrite.**
> - For `"env"`: add/update only the keys shown below. Keep all existing env vars intact.
> - For `"hooks"`: for each event type, **append** the TMA1 hook entry to the existing array. Do NOT replace the array or remove other hooks.
> - For all other top-level keys (`permissions`, `mcpServers`, `enabledPlugins`, etc.): do NOT touch them.
>
> Example merge for a hook event that already has entries:
> ```json
> "PreToolUse": [
>   { "hooks": [{ "type": "command", "command": "existing-hook.sh" }] },
>   { "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }
> ]
> ```

The following keys need to be present (add if missing, do not remove others):

```json
{
  "env": {
    "CLAUDE_CODE_ENABLE_TELEMETRY": "1",
    "CLAUDE_CODE_ENHANCED_TELEMETRY_BETA": "1",
    "OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:14318/v1/otlp",
    "OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
    "OTEL_METRICS_EXPORTER": "otlp",
    "OTEL_LOGS_EXPORTER": "otlp",
    "OTEL_TRACES_EXPORTER": "otlp"
  },
  "hooks": {
    "SessionStart": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "SessionEnd": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "PreToolUse": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "PostToolUse": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "PostToolUseFailure": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "UserPromptSubmit": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "SubagentStart": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "SubagentStop": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "Notification": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "Stop": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "PreCompact": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "PostCompact": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "PermissionRequest": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "PermissionDenied": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "TaskCreated": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "TaskCompleted": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "FileChanged": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "CwdChanged": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "InstructionsLoaded": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "Elicitation": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "ElicitationResult": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "WorktreeCreate": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "WorktreeRemove": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "StopFailure": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "Setup": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "TeammateIdle": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }],
    "ConfigChange": [{ "hooks": [{ "type": "command", "command": "~/.tma1/hooks/tma1-hook.sh", "timeout": 3 }] }]
  }
}
```

Claude Code exports metrics, logs, and traces (when enhanced telemetry enabled). `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1` enables trace spans (TTFT, tool timing, permission waits) for the Traces tab and waterfall visualization.

The `hooks` section uses command hooks (via `~/.tma1/hooks/tma1-hook.sh`, auto-installed by tma1-server on startup) for all 27 event types. On Windows, use `%USERPROFILE%\.tma1\hooks\tma1-hook.ps1` instead. This enables session-level conversation tracking (tool calls, subagent hierarchy, context compaction, permission flow, file changes) in the Sessions view.

#### Codex

Add to `~/.codex/config.toml` (Windows: `%USERPROFILE%\.codex\config.toml`):

```toml
[otel]
log_user_prompt = true

[otel.exporter.otlp-http]
endpoint = "http://localhost:14318/v1/logs"
protocol = "binary"

[otel.trace_exporter.otlp-http]
endpoint = "http://localhost:14318/v1/traces"
protocol = "binary"

[otel.metrics_exporter.otlp-http]
endpoint = "http://localhost:14318/v1/metrics"
protocol = "binary"
```

Codex uses separate exporters for logs, traces, and metrics. Restart Codex after config changes.

Codex session logs are automatically stored at `~/.codex/sessions/` in JSONL format. TMA1 can parse these for conversation replay in the Sessions view — no additional configuration needed beyond OTel setup above.

#### GitHub Copilot CLI

**Zero config.** TMA1 auto-discovers Copilot CLI session logs at `~/.copilot/session-state/<sessionId>/events.jsonl` and parses them into the Sessions and Prompts views. Just run `tma1-server` and use Copilot CLI as usual. No environment variables, no config file edits, no restart needed.

What gets captured: session start/end, user prompts, assistant messages (content + reasoning → thinking), tool calls with success/failure, subagent lifecycle (with model / tokens / duration metadata), and skill invocations.

#### Any OTel SDK

```bash
# macOS / Linux
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:14318/v1/otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
# Then run your agent as usual
```

```powershell
# Windows (PowerShell)
$env:OTEL_EXPORTER_OTLP_ENDPOINT = "http://localhost:14318/v1/otlp"
$env:OTEL_EXPORTER_OTLP_PROTOCOL = "http/protobuf"
# Then run your agent as usual
```

**Important:** After configuring, restart the agent so the new OTel config takes effect.

---

### Step 4 — Restart the agent

Restart the agent so the new OTel config takes effect.

---

### Step 5 — Verify data is flowing

Wait ~1 minute after restart, then check:

```bash
curl -sf http://localhost:14318/status | python3 -m json.tool
```

Expected response:
```json
{
  "status": "ok",
  "greptimedb": "running",
  "dashboard": "http://localhost:14318"
}
```

Also check if data has arrived:

```bash
curl -s -X POST http://localhost:14318/api/query \
  -H "Content-Type: application/json" \
  -d '{"sql": "SHOW TABLES"}' \
  | python3 -m json.tool
```

If you see `opentelemetry_logs`, `opentelemetry_traces`, `openclaw_*`, `claude_code_*`, `codex_*`, `tma1_hook_events`, or `tma1_messages` tables, data is flowing.

---

### Step 6 — Handoff

`[AGENT]` After successful setup, send this handoff to the user.
Translate into the user's language while keeping the structure.

```
✅ TMA1 is running.

📊 DASHBOARD
Open: http://localhost:14318

🔌 QUERY API
All SQL queries go through POST with JSON body:

  curl -s -X POST http://localhost:14318/api/query \
    -H 'Content-Type: application/json' \
    -d '{"sql": "SHOW TABLES"}'

Note: table names containing uppercase letters (e.g. "claude_code_cost_usage_USD_total")
must be quoted with double quotes in SQL.

🔍 QUICK QUERIES

-- Claude Code: today's cost by model (from logs, not metrics — counters reset per session)
SELECT json_get_string(log_attributes, 'model') AS model,
  ROUND(SUM(json_get_float(log_attributes, 'cost_usd')), 4) AS cost_usd
FROM opentelemetry_logs WHERE body = 'claude_code.api_request'
  AND timestamp >= DATE_TRUNC('day', NOW())
GROUP BY model ORDER BY cost_usd DESC;

-- Codex: recent requests
SELECT timestamp, json_get_string(log_attributes, 'model') AS model,
  json_get_int(log_attributes, 'input_token_count') AS input_tok,
  json_get_int(log_attributes, 'output_token_count') AS output_tok
FROM opentelemetry_logs WHERE scope_name LIKE 'codex_%'
  AND json_get_int(log_attributes, 'input_token_count') IS NOT NULL
ORDER BY timestamp DESC LIMIT 10;

-- OpenClaw: token usage by model
SELECT openclaw_model AS model, openclaw_token AS token_type, SUM(greptime_value) AS tokens
FROM openclaw_tokens_total WHERE greptime_timestamp > NOW() - INTERVAL '1 day'
GROUP BY openclaw_model, openclaw_token ORDER BY tokens DESC;

-- Any agent: list all tables
SHOW TABLES;

💾 YOUR DATA
Stored locally in: ~/.tma1/data/ (Windows: %USERPROFILE%\.tma1\data\)

Database config: ~/.tma1/config/standalone.toml (Windows: %USERPROFILE%\.tma1\config\standalone.toml)
Edit this file if you want to tune database resource usage, then restart `tma1-server`.
Never sent to any cloud service.

♻️ RESTART / UPGRADE
Restart: tma1-server
Upgrade (macOS/Linux): curl -fsSL https://tma1.ai/install.sh | bash
Upgrade (Windows PS):  irm https://tma1.ai/install.ps1 | iex
  (stops the running service, downloads the new binary, restarts — data is preserved)
Force reinstall (wipes all data): curl -fsSL https://tma1.ai/install.sh | TMA1_FORCE=1 bash

💡 For more queries, read: https://tma1.ai/REFERENCE.md
```

---

## Persistent processes — wrap with `tma1 build --watch`

`[AGENT]` For commands that **run indefinitely** until the user stops
them (`npm run dev`, `cargo watch`, `pytest --watch`, `hugo serve`,
`docker compose up`), wrap with `tma1 build --watch`:

```bash
tma1 build --watch --tag dev -- npm run dev
tma1 build --watch --tag watch -- cargo watch -x test
```

For one-shot commands (`make test`, `go test ./...`, `pytest`,
`cargo build`), **don't bother** — just use the `Bash` tool directly.
You already get the full output inline in the tool result, and any
failure is visible to you immediately.

### Why `tma1 build --watch` is worth it for persistent processes

The `Bash` tool's `run_in_background` mode can keep a dev server alive,
but its output is fragmented across `BashOutput` polls and disappears
when the session ends. `tma1 build --watch` solves that:

- **Persistence beyond your session** — output is written to
  `tma1_build_events`; the next agent session (or another agent / the
  human at the dashboard) reads it via the `get_build_status` MCP tool
  without needing to be attached to your original process.
- **Time-debounced flush + signal forwarding** — flushes buffered output
  every 2s (override with `--debounce`) instead of waiting for a line
  threshold, and forwards SIGINT/SIGTERM so `Ctrl-C` cleanly stops the
  dev server.
- **Anomaly detection** — `tma1_build_events` feeds the
  `repeated_failed_build` and `build_broken_after_my_edit` rules, which
  surface in the next `<tma1-context>` injection if the dev server
  starts emitting failures.

Run `tma1 build --help` for the full flag list.

---

## Troubleshooting

`[AGENT]` When diagnosing issues, check logs first, then work through the common problems below.

### Where to find logs

- **TMA1 server**:
  - macOS: `~/Library/Logs/tma1-server.log`
  - Linux: `journalctl --user -u tma1-server`
  - Windows: no log file by default — run `tma1-server` manually in a terminal to see output
  - Debug mode: `TMA1_LOG_LEVEL=debug tma1-server`
- **GreptimeDB**: `~/.tma1/data/logs/` (log files rotated automatically, up to 168 files)

`[AGENT]` Read the relevant log file to diagnose the issue before suggesting fixes to the user.

### Dashboard shows "Unhealthy" but error rate is 0%

By default, generic API services use latency thresholds where p95 > 2s is yellow and p95 > 5s is red (unhealthy). For OpenClaw, `oc_updateHealthIndicator()` overrides these defaults to p95 > 10s (yellow) and p95 > 30s (red) to better match typical LLM/agent call durations.

**What to check:** Focus on the error rate, not latency color. If error rate is 0% and your requests are completing successfully, the service is healthy — long-running LLM calls can still exceed the configured latency thresholds even when the system is functioning as expected.

### OpenClaw — Frequent "session.stuck" warnings

OpenClaw emits `openclaw.session.stuck` spans when a session stays in `processing` state longer than `diagnostics.stuckSessionWarnMs`. The default timeout is short and triggers false positives during long-running agent tasks.

**Fix:** Ask the user if they want to increase the stuck-session warning threshold:

```bash
# 120s — good for most long-running tasks
openclaw config set diagnostics.stuckSessionWarnMs 120000
# 300s — for very long tasks (large codebases, multi-step workflows)
openclaw config set diagnostics.stuckSessionWarnMs 300000
openclaw gateway restart
```

### No data showing after setup

1. Verify TMA1 is running: `curl -sf http://localhost:14318/health`
2. Verify database is healthy: `curl -sf http://localhost:14000/health`
3. Check if tables exist:
   ```bash
   curl -s -X POST http://localhost:14318/api/query \
     -H "Content-Type: application/json" \
     -d '{"sql": "SHOW TABLES"}' | python3 -m json.tool
   ```
4. If no tables: the agent hasn't sent any data yet. Ensure the agent was restarted after configuring the OTel endpoint (Step 4), then wait ~1 minute and check again.
5. If tables exist but dashboard is empty: check the time range selector — data might be outside the selected window.

### Claude Code — Hook events not appearing in Sessions view

1. Verify the hook script exists: `ls ~/.tma1/hooks/tma1-hook.sh`
2. Verify `~/.claude/settings.json` has the `hooks` section (see Step 3 — Claude Code)
3. Test the hook manually: `echo '{"type":"test"}' | ~/.tma1/hooks/tma1-hook.sh`
4. If the script is missing, restart `tma1-server` — it auto-installs the hook script on startup.

### Port already in use

If tma1-server fails with `bind: address already in use`:

1. Check which process holds the port:
   ```bash
   # macOS / Linux
   lsof -i :14318
   # Windows
   netstat -ano | findstr 14318
   ```
2. If it's a previous tma1-server instance, kill it and restart.
3. If another service uses the port, change TMA1's port via environment variable:
   ```bash
   TMA1_PORT=14319 tma1-server
   ```
   GreptimeDB ports can also be changed: `TMA1_GREPTIMEDB_HTTP_PORT`, `TMA1_GREPTIMEDB_GRPC_PORT`, `TMA1_GREPTIMEDB_MYSQL_PORT`.

### GreptimeDB did not become healthy

If startup fails with `greptimedb: did not become healthy: timeout after 30s`:

1. Check if port 14000 is already in use: `lsof -i :14000`
2. Check GreptimeDB logs: `ls ~/.tma1/data/logs/` and read the latest log file
3. If the binary is corrupted, remove it and restart (tma1 will re-download):
   ```bash
   rm ~/.tma1/bin/greptime
   tma1-server
   ```

---

## Query Reference

For the complete SQL query catalog, troubleshooting, and examples, see:
[REFERENCE.md](https://tma1.ai/REFERENCE.md)
