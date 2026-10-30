# Anomaly engine

Four rules run on `Detector.Detect`. Findings are advisory and use the
`user_prompt_submit` channel. None of the current rules blocks the Stop hook.
Historical findings retain their original kind and severity in the emit log.

## Rules

| Kind | Severity | Evidence |
|------|----------|----------|
| `stale_file_view` | high | An observed Edit follows an external file change without an intervening successful Read of that file. |
| `repeated_failed_build` | medium | A recognized build/test command has at least three observed failures after its most recent observed success, within 30 minutes. |
| `external_modified_during_session` | medium | Files in the session project changed outside observed agent writes within the session window and the last 30 minutes. |
| `context_pressure` | medium | The latest nonzero input-usage sample in the last 30 minutes reaches `TMA1_CONTEXT_PRESSURE_THRESHOLD` (default 700,000). |

Repeated build failures are grouped by the full command and working directory.
A successful unrelated command does not clear them. Detection inspects the
latest 1,000 completed Bash events in the window. Commands with missing or
invalid command JSON or working directory are excluded. Known direct build
and test invocations are recognized; arbitrary shell wrappers are not parsed.
Failures do not establish an identical error, a retry loop, or a cause.
The former `build_broken_after_my_edit` and `test_stuck` rules no longer emit.

Claude Code and OpenCode input usage includes input, cache-read, and
cache-creation counters. Codex input already includes cache tokens, which are
not added again. Other or unknown sources are not evaluated by this rule. Usage rows
may contain streaming deltas; the sample is not an exact context-window
measurement. Output-only samples do not replace the last nonzero input sample.
The configured threshold is absolute, not a percentage of an inferred model
limit. Context-pressure advice asks the agent to check its own context indicator.
It does not require a commit or a new session.

### Stale file views

For each observed Edit, the detector finds the latest Read before it and checks
for an external change between that Read and the Edit. File events and external
changes are queried separately and matched by path. Reads require a successful
PostToolUse event; edits use PreToolUse observations. A successful reread after
the external change clears the stale-view finding.

## Suppression

The candidate cache lasts 30 seconds. Channel selection precedes suppression;
a Stop or PostToolUse request does not consume a next-prompt finding. Findings with the same kind, severity, files,
and command identity are suppressed for 10 minutes per session. Build failures
stop qualifying after a successful run of that same command in the same working
directory. They must accumulate three new failures to qualify again.

The stale-view rule retains its existing resolution check: a later successful
Read resets its suppression history. External-change findings expire with the
30-minute observation window. Context-pressure findings stop qualifying when a
new nonzero input sample falls below the threshold or the observation expires.

`DetectPreview` reads suppression state without advancing emission history.
Hook injection uses `Detect`; MCP queries use the read-only preview path.

## Emit log and dashboard

Emitted findings are appended to `tma1_anomaly_emits` with the session ID,
kind, severity, channel, evidence, suggestion, related files, and first-emission
time. The dashboard polls emission history every 10 seconds. Historical rows
are not a live list of unresolved conditions.

The following endpoints report emission volume and subsequent actions:

- `GET /api/anomalies/budget?days=7&budget=5`
- `GET /api/anomalies/follow-rate?days=7&window=5`

Following a suggestion does not establish that the finding was correct or
that the underlying problem was resolved. Precision requires reviewing the
finding alongside its source events.

## Configuration

| Environment variable | Default | Effect |
|----------------------|---------|--------|
| `TMA1_CONTEXT_PRESSURE_THRESHOLD` | `700000` | Absolute input-usage threshold |
| `TMA1_DISABLE_INJECTION` | unset | `1` disables hook injection |

SQL observation windows and the suppression interval are constants in
`server/internal/perception/anomaly.go`.
