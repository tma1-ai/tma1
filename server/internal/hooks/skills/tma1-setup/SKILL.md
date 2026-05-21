---
name: tma1-setup
description: "Install and configure TMA1 local observability (Claude Code, Codex, GitHub Copilot CLI, OpenClaw, any OTel SDK). Use when the user says: install tma1, setup observability, monitor my agent, track token usage, set up telemetry."
context: fork
allowed-tools: Bash
---

# TMA1 Setup

You are helping the user install and configure TMA1, a local-first LLM agent observability tool.

## Step 1: Check if TMA1 is already running

```bash
curl -sf http://localhost:14318/health
```

If this returns `{"status":"ok"}`, TMA1 is already running. Skip to Step 4.

## Step 2: Install TMA1

Download and install the tma1-server binary:

```bash
# macOS / Linux
curl -fsSL https://tma1.ai/install.sh | bash
```

```powershell
# Windows (PowerShell)
irm https://tma1.ai/install.ps1 | iex
```

This installs the binary to `~/.tma1/bin/tma1-server` (or `%USERPROFILE%\.tma1\bin\tma1-server.exe` on Windows).

## Step 3: Start TMA1

On macOS/Linux the installer registers a service that auto-starts.
On Windows the installer registers a Scheduled Task that auto-starts.

If TMA1 is not running, start it manually:

```bash
# macOS / Linux
~/.tma1/bin/tma1-server &
```

```powershell
# Windows (PowerShell)
Start-Process "$env:USERPROFILE\.tma1\bin\tma1-server.exe"
```

Wait for GreptimeDB to become healthy:

```bash
# macOS / Linux
for i in $(seq 1 30); do
  if curl -sf http://localhost:14318/health > /dev/null 2>&1; then
    echo "TMA1 is ready."
    break
  fi
  sleep 1
done
```

```powershell
# Windows (PowerShell)
for ($i = 0; $i -lt 30; $i++) {
  try { if ((Invoke-WebRequest -Uri http://localhost:14318/health -UseBasicParsing).StatusCode -eq 200) { Write-Host "TMA1 is ready."; break } } catch {}
  Start-Sleep -Seconds 1
}
```

If it does not become healthy after 30 seconds, check the logs and report the error.

## Step 4: Verify OTel endpoint

Confirm GreptimeDB is accepting OTLP data:

```bash
curl -sf http://localhost:14318/status
```

This should return `{"status":"ok","greptimedb":"running",...}`.

## Step 5: Configure the agent

Tell the user to set the OTel exporter endpoint. The exact method depends on their agent:

**Claude Code** — merge into `~/.claude/settings.json` (Windows: `%USERPROFILE%\.claude\settings.json`):

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

Claude Code exports metrics, logs, and traces (enhanced telemetry). `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1` enables trace spans with TTFT, tool timing, and permission waits for the Traces tab and waterfall. The `hooks` use command hooks (via `~/.tma1/hooks/tma1-hook.sh`, auto-installed by tma1-server on startup) for all 27 event types. On Windows, use `%USERPROFILE%\.tma1\hooks\tma1-hook.ps1` instead.

**Codex** — add to `~/.codex/config.toml` (Windows: `%USERPROFILE%\.codex\config.toml`):
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

**OpenClaw**:
```bash
openclaw config set diagnostics.enabled true
openclaw config set diagnostics.otel.enabled true
openclaw config set diagnostics.otel.endpoint http://localhost:14318/v1/otlp
openclaw config set diagnostics.otel.traces true
openclaw config set diagnostics.otel.metrics true
openclaw gateway restart
```

Session transcripts at `~/.openclaw/agents/*/sessions/*.jsonl` are auto-discovered — no extra setup needed for Sessions and Prompts views.

**GitHub Copilot CLI** — zero config. TMA1 auto-discovers session logs at `~/.copilot/session-state/<sessionId>/events.jsonl`. Nothing to configure; just run `tma1-server` and use Copilot CLI normally. Sessions, tool calls (with failure detection), subagent lifecycle, and skill invocations all show up in the Copilot CLI dashboard view and the unified Sessions view.

**Other OTel-compatible agents** (standard GenAI SDK) — typically export traces:
```bash
# macOS / Linux
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:14318/v1/otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
```

```powershell
# Windows (PowerShell)
$env:OTEL_EXPORTER_OTLP_ENDPOINT = "http://localhost:14318/v1/otlp"
$env:OTEL_EXPORTER_OTLP_PROTOCOL = "http/protobuf"
```

## Step 6: Verify data flow

After the user runs at least one agent interaction with the endpoint configured:

```bash
curl -s -X POST http://localhost:14318/api/query \
  -H 'Content-Type: application/json' \
  -d '{"sql": "SHOW TABLES"}' 2>/dev/null | python3 -m json.tool
```

If you see `opentelemetry_logs`, `opentelemetry_traces`, `openclaw_*`, `claude_code_*`, `codex_*`, `tma1_hook_events`, or `tma1_messages` tables, data is flowing. For Copilot CLI specifically, check that `tma1_hook_events` has rows with `agent_source = 'copilot_cli'`.

## Handoff

Tell the user:

```
Dashboard: http://localhost:14318

Query API — all SQL queries go through POST with JSON body:
  curl -s -X POST http://localhost:14318/api/query \
    -H 'Content-Type: application/json' \
    -d '{"sql": "SHOW TABLES"}'

OTel endpoint: http://localhost:14318/v1/otlp
Use /tma1 to query observability data inline without opening the dashboard.
For more queries: https://tma1.ai/SKILL.md
```
