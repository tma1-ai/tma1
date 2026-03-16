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
| **Claude Code** | OTel metrics + logs | Token usage, cost, active time, tool decisions, API requests, user prompts |
| **Codex** | OTel logs + traces + metrics | User prompts, LLM calls, tool executions, token usage (separate exporters for logs/traces/metrics) |
| **OpenClaw** | OTel traces + metrics | LLM calls (model, tokens, cache), messages, webhooks, sessions, queue depth |
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

- If `RUNNING`: skip to Step 3 (configure agent).
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

#### Claude Code

Add to `~/.claude/settings.json`:

```json
{
  "env": {
    "OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:14318/v1/otlp",
    "OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
    "OTEL_METRICS_EXPORTER": "otlp",
    "OTEL_LOGS_EXPORTER": "otlp"
  }
}
```

Claude Code exports metrics and logs (not traces). The metrics/logs exporters must be explicitly enabled.

#### Codex

Add to `~/.codex/config.toml`:

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

#### Any OTel SDK

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:14318/v1/otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
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

If you see `opentelemetry_logs`, `opentelemetry_traces`, `openclaw_*`, or `claude_code_*` tables, data is flowing.

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
Stored locally in: ~/.tma1/data/

Database config: ~/.tma1/config/standalone.toml
Edit this file if you want to tune database resource usage, then restart `tma1-server`.
Never sent to any cloud service.

♻️ RESTART / UPGRADE
Restart: tma1-server
Upgrade (macOS/Linux): curl -fsSL https://tma1.ai/install.sh | bash
Upgrade (Windows PS):  irm https://tma1.ai/install.ps1 | iex
  (stops the running service, downloads the new binary, restarts — data is preserved)

💡 For more queries, read: https://tma1.ai/REFERENCE.md
```

---

## Query Reference

For the complete SQL query catalog, troubleshooting, and examples, see:
[REFERENCE.md](https://tma1.ai/REFERENCE.md)
