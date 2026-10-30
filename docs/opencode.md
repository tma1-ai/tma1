# OpenCode

TMA1 accepts the official OpenCode v1 session export format. The integration
is checked against the v1.19.5 schema and SDK. Sessions appear in Overview,
Sessions, comparison, Prompts, and MCP session search with source `opencode`.

## Automatic import

Copy the plugin from this repository and restart OpenCode:

```bash
mkdir -p ~/.config/opencode/plugins
cp integrations/opencode/tma1.js ~/.config/opencode/plugins/tma1.js
```

The plugin exports the session when OpenCode emits `session.idle`, then posts
it to `http://127.0.0.1:14318/api/import/opencode`. Change that URL in the plugin
if TMA1 uses a different local port. Import failures are logged; the next idle
event retries the snapshot.

## Import an existing session

```bash
opencode export SESSION_ID > session.json
curl --fail-with-body http://127.0.0.1:14318/api/import/opencode \
  -H 'Content-Type: application/json' --data-binary @session.json
```

The response contains the TMA1 session ID (`oc:SESSION_ID`) and inserted record
count. Reimporting a snapshot skips persisted records, including after a TMA1
restart. A failed import may have committed some records; retry the same file.
Requests are limited to 20 MiB.

## Recorded data

- User and completed assistant text, reasoning, and terminal tool results.
- Tool success/failure and source timestamps.
- Completed message usage, without duplicating step-finish counters.
- Input, cache reads, and cache writes as separate counters. Output includes
  reasoning exactly once.

Pending assistant messages and tools are skipped until a later snapshot.
Synthetic and ignored text parts are excluded. Attachments, file diffs,
provider billing totals, and parent/child session linking are not imported.
Import does not install TMA1 hooks or inject context into OpenCode. Previously
imported records are not rewritten after OpenCode compacts or edits a session.

Sources: [export implementation](https://github.com/anomalyco/opencode/blob/v1.19.5/packages/opencode/src/cli/cmd/export.ts),
[v1 schema](https://github.com/anomalyco/opencode/blob/v1.19.5/packages/schema/src/v1/session.ts),
[token normalization](https://github.com/anomalyco/opencode/blob/v1.19.5/packages/opencode/src/session/session.ts),
[plugins](https://opencode.ai/docs/plugins/).
