# Anomaly engine

Six rules run on every `Detector.Detect`, with a 10-minute per-session
suppression layer and per-rule resolution checks. Each anomaly carries
a `Channel` that decides where it lands in the agent's prompt stream.

## Channels

| Channel | Where it shows up | Use when |
|---------|-------------------|----------|
| `user_prompt_submit` | Next turn's prepended `<tma1-context>` digest | The agent can read it before its next reasoning step. Default for everything that isn't blocking. |
| `stop_block` | `Stop` hook returns `{"decision":"block","reason":"…"}` | Unresolved issue would cause concrete harm if the agent stopped now (broken build, repeated identical failure). HIGH severity only. |
| `post_tool_use` | Appended to the offending tool result | No rule currently uses this. PostToolUse(Read) of an externally-modified file is handled via R-stale-view → next UserPromptSubmit instead. Reserved for rules that genuinely need a same-turn signal. |

Bundle filtering (`bundle.Anomalies`) only carries
`user_prompt_submit`-channel anomalies. Stop-block findings stay out
of the bundle so the same issue never shows up twice (once in the
digest, once in the block reason).

## The six rules

| Kind | Severity | Channel | Triggers when |
|------|----------|---------|---------------|
| `stale_file_view` | high | user_prompt_submit | Agent Edited file F where the latest Read of F was before an external change of F. See [Plan scenario](#r-stale-view) below. |
| `build_broken_after_my_edit` | high if ≥3 failures, else medium | stop_block (HIGH) / user_prompt_submit (MEDIUM) | Agent edited file F in the last 30 min, then a Bash PostToolUseFailure within ±10 min mentioned F's basename in input or output. |
| `repeated_failed_build` | high | stop_block | Same Bash command prefix (first 60 chars) failed 3+ times in the last 30 min. |
| `test_stuck` | medium | user_prompt_submit | Same test identifier (regex-extracted from "FAIL …") appeared in Bash PostToolUseFailure output 3+ times in the last 30 min. |
| `external_modified_during_session` | medium | user_prompt_submit | At least one file changed outside observed agent writes on this project within the session window, and the change is no older than 30 min. |
| `context_pressure` | medium | user_prompt_submit | `SUM(input_tokens) >= TMA1_CONTEXT_PRESSURE_THRESHOLD` (default 100,000 ≈ 50% of Claude Sonnet's 200k window). One-shot per session. |

### R-stale-view

Plan scenario:

```
T1 — agent Reads foo.go
T2 — foo.go changes externally          (T2 > T1)
T3 — agent Edits foo.go with no Re-Read in (T2, T3)
```

The Edit is based on a stale in-memory view. The rule pulls Read /
Edit / Write events and external changes separately, then merges in
Go: for each Edit at T_edit, find the most recent Read strictly
before T_edit; if any external change falls in `(lastRead, T_edit)`,
the Edit was stale.

The merge logic is factored into a pure helper, `detectStaleEdit`,
covered by six unit cases in `anomaly_test.go`.

## Suppression and resolution

`anomalyCache.suppressWithResolution` is the full decision step. Per
candidate:

- **Fresh key** → emit. Stamps `FirstEmittedAt`.
- **Same key, within 10 min of last emit, resolution NOT observed** → silent.
- **Same key, resolution observed since last emit** → reset emit state, emit again with refreshed `FirstEmittedAt`.
- **Same key, after the 10-min silence window** → emit. Keeps `FirstEmittedAt` from the original.

Resolution checks run outside the cache lock so SQL latency doesn't
serialise concurrent `Detect` calls.

| Rule | Resolution check |
|------|------------------|
| `stale_file_view` | Agent did a PreToolUse Read on the related file after the last emit. |
| `build_broken_after_my_edit` | Any PostToolUse Bash (non-failure) after the last emit. |
| `repeated_failed_build` | Same as above. |
| `test_stuck` | Same as above. |
| `external_modified_during_session` | None — time-based decay (30 min). |
| `context_pressure` | None — one-shot per session. |

## Emit log

Every kept anomaly is asynchronously logged into `tma1_anomaly_emits`
(append-only). One row per emission, columns:

```
ts, session_id, kind, severity, channel,
evidence, suggestion, related_files (json),
first_emitted_at
```

This is the source data for the three validation gates.

## Validation gates

| Gate | Target | How to check |
|------|--------|--------------|
| Precision | ≥ 70% TP on a sampled set | `SELECT * FROM tma1_anomaly_emits ORDER BY ts DESC LIMIT 50` — label TP/FP manually |
| Daily emit budget | ≤ 5 emits / Kind / day | `GET /api/anomalies/budget?days=7&budget=5` |
| Action follow-rate | ≥ 30% within 5 tool calls | `GET /api/anomalies/follow-rate?days=7&window=5` |

A rule that fails its gate is a candidate for severity adjustment,
suppression-window tightening, or removal. The bar for adding new
rules is gate compliance first; precision second; volume third.

## Dashboard delivery: polling, not SSE

The plan originally called for SSE broadcast of anomaly toasts. The
implementation polls `/api/anomalies` every 10 s from both the
Anomalies dashboard tab and the Agent Canvas overlay. The Detector
caches its result for 30 s per session (`anomalyCache.ttl`), so the
client-side poll cadence amortises against the cache: most polls
short-circuit on the cached set, and the worst-case staleness is
~10 s — well under the human review threshold.

Why polling won: the SSE path required a second broadcast channel
parallel to `/api/hooks/stream` (which is reserved for tool events),
with its own subscriber lifecycle and reconnect logic. Anomalies fire
at < 5 / kind / day per the validation budget — a steady-state poll is
cheaper than maintaining the push channel.

## Tuning knobs

| Env var | Default | Effect |
|---------|---------|--------|
| `TMA1_CONTEXT_PRESSURE_THRESHOLD` | `100000` | Tokens before `context_pressure` fires |
| `TMA1_DISABLE_INJECTION` | unset | When `1`, hooks return empty bodies (no injection) |

The suppression window (10 min) and per-rule SQL windows (mostly 30
min) are constants in `server/internal/perception/anomaly.go`. Change
them in code, not env, so a misconfiguration can't make a rule never
silence.
