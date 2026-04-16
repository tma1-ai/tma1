---
name: tma1
version: 0.2.0
description: |
  Local-first LLM agent observability.

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

keywords:
  - tma1
  - observability
  - token usage
  - cost tracking
  - agent monitoring
  - local telemetry
  - otel
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
│   your agent runs. tma1 remembers.                           │
│                                                              │
└──────────────────────────────────────────────────────────────┘
```

# TMA1

TMA1 gives you local-first observability for your AI agent.
Token usage, cost, latency — stored locally, queryable with plain SQL.
No cloud account. No Docker. No Grafana setup.
Works with Claude Code, Codex, OpenClaw, or any OTel-enabled agent.

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
| **OpenClaw** | OTel traces + metrics + session JSONL | LLM calls (model, tokens, cache), messages, webhooks, sessions, queue depth, session conversations |
| **Other (GenAI SDK)** | OTel traces + logs | Token usage, cost, latency, conversation replay, prompt injection detection (GenAI semantic conventions) |

The dashboard auto-detects the data source and shows the right view.

All data is stored locally in `~/.tma1/data/` and never leaves your machine.

Dashboard: **http://localhost:14318**

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
