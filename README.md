# TMA1

> *"Your agent runs. TMA1 remembers."*

Local-first observability for AI agents.
Track token usage, cost, latency, errors, and security signals across your AI agents.
No cloud account, no Docker, no Grafana setup.

Named after TMA-1 (Tycho Magnetic Anomaly-1) from *2001: A Space Odyssey*:
the monolith buried on the moon, silently recording everything until you dig it out.

## What You Get

- **Cost breakdown**: token counts and estimated cost per model, with burn-rate projections and cache efficiency
- **Latency tracking**: p50/p95 percentiles per model, tool performance tables
- **Security monitoring**: shell command detection, prompt injection alerts, webhook error tracking
- **Conversation replay**: inspect prompts and responses (where supported, e.g. Claude Code)
- **Anomaly detection**: flags unusual token counts, high error rates, or slow responses
- **Full-text search**: search across all recorded events and traces
- **SQL access**: query raw events directly via MySQL protocol or built-in query UI

## Quick Install

```bash
# macOS / Linux
curl -fsSL https://tma1.ai/install.sh | bash

# Windows (PowerShell)
irm https://tma1.ai/install.ps1 | iex
```

Or build from source:

```bash
git clone https://github.com/tma1-ai/tma1.git
cd tma1
make build
```

## Quick Start

```bash
# Start TMA1
tma1-server

# Configure your agent to send OTel data (protobuf required):

# Claude Code — add to ~/.claude/settings.json:
#   "env": {
#     "OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:14318/v1/otlp",
#     "OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
#     "OTEL_METRICS_EXPORTER": "otlp",
#     "OTEL_LOGS_EXPORTER": "otlp"
#   }

# OpenClaw (sends traces)
openclaw config set diagnostics.otel.endpoint http://localhost:14318/v1/otlp

# Codex — add to ~/.codex/config.toml:
#   [otel]
#   log_user_prompt = true
#
#   [otel.exporter.otlp-http]
#   endpoint = "http://localhost:14318/v1/logs"
#   protocol = "binary"
#
#   [otel.trace_exporter.otlp-http]
#   endpoint = "http://localhost:14318/v1/traces"
#   protocol = "binary"
#
#   [otel.metrics_exporter.otlp-http]
#   endpoint = "http://localhost:14318/v1/metrics"
#   protocol = "binary"
#
#   Then restart Codex.

# Any OTel SDK
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:14318/v1/otlp \
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf \
your-agent

# Open the dashboard
open http://localhost:14318
```

## Supported Sources

- **Claude Code**: OTel metrics + logs
- **Codex**: OTel logs + traces, plus native metrics when `otel.metrics_exporter` is enabled
- **OpenClaw**: OTel traces + metrics
- **Any OTel-compatible GenAI app**: traces with `gen_ai.*` attributes

Codex uses separate OTLP exporters per signal. In practice, configure logs, traces,
and metrics with their own direct endpoints rather than a single `/v1/otlp` base URL.

Send OTLP to:

```text
http://localhost:14318/v1/otlp
```

Or direct signal endpoints (also supported):

```text
http://localhost:14318/v1/logs
http://localhost:14318/v1/traces
http://localhost:14318/v1/metrics
```

## How It Works

1. Your agent sends OTLP data to `tma1-server`.
2. TMA1 stores and aggregates data locally.
3. Dashboard is served from the same process on `http://localhost:14318`.
4. You can query data via SQL.

Implementation detail: TMA1 uses an embedded local GreptimeDB process managed by `tma1-server`.

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `TMA1_HOST` | `127.0.0.1` | Address tma1-server binds to |
| `TMA1_PORT` | `14318` | HTTP port for tma1-server dashboard |
| `TMA1_DATA_DIR` | `~/.tma1` | Local data and binary directory |
| `TMA1_GREPTIMEDB_VERSION` | `latest` | GreptimeDB version to download |
| `TMA1_GREPTIMEDB_HTTP_PORT` | `14000` | Embedded database HTTP API + OTLP port |
| `TMA1_GREPTIMEDB_GRPC_PORT` | `14001` | GreptimeDB gRPC port |
| `TMA1_GREPTIMEDB_MYSQL_PORT` | `14002` | GreptimeDB MySQL protocol port |
| `TMA1_LOG_LEVEL` | `info` | Log level: debug/info/warn/error |
| `TMA1_DATA_TTL` | `15d` | Default TTL for auto-created tables |

On first start, TMA1 writes a default GreptimeDB config to `~/.tma1/config/standalone.toml`, then starts GreptimeDB with `-c`. The default keeps `http`, `mysql`, and Prometheus Remote Storage enabled, disables Postgres, InfluxDB, OpenTSDB, and Jaeger, and applies conservative local resource limits.

## Development

```bash
make build           # Build the binary
make build-windows   # Cross-compile for Windows
make vet             # Run go vet
make test            # Run tests with race detector
make run             # Build and run locally
```

## License

Apache-2.0
