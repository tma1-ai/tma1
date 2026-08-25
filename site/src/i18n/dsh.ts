import type { CodeBlock, ProductCopy } from './product-copy';

const REPO = 'https://github.com/tma1-ai/dsh-otel';
const NPM = 'https://www.npmjs.com/package/@tma1-ai/dsh-plugin-greptimedb';
const DSH = 'https://github.com/deepseek-ai/deepseek-harness';
const GREPTIMEDB = 'https://github.com/GreptimeTeam/greptimedb';
const SHOT = '/products/dsh-otel';

/** Same query in every locale — only the leading comment is translated. */
const joinSQL = (comment: string): CodeBlock => ({
  title: 'greptimedb · sql',
  lines: [
    { kind: 'comment', text: `-- ${comment}` },
    { text: 'SELECT tool.span_name,' },
    { text: '       chat."span_attributes.gen_ai.request.model" AS model,' },
    { text: '       tool.duration_nano / 1000000 AS ms' },
    { text: 'FROM opentelemetry_traces AS tool' },
    { text: 'JOIN opentelemetry_traces AS chat' },
    { text: '  ON  chat.trace_id = tool.trace_id' },
    { text: '  AND chat."span_attributes.dsh.step" = tool."span_attributes.dsh.step"' },
    { text: '  AND chat.span_name LIKE \'chat%\'' },
    { text: 'WHERE tool.span_name LIKE \'execute_tool%\'' },
    { text: 'ORDER BY tool.duration_nano DESC' },
    { text: 'LIMIT 10;' },
  ],
});

const spanTree = (title: string): CodeBlock => ({
  title,
  lines: [
    { text: 'invoke_agent dsh              turn/start → turn/end' },
    { text: '├── chat deepseek-chat        step/start → assistant/message' },
    { text: '├── execute_tool bash         tool/call  → tool/result' },
    { text: '└── chat deepseek-chat' },
  ],
});

const tokenMath = (title: string, note: string): CodeBlock => ({
  title,
  lines: [
    { text: 'gen_ai.usage.input_tokens  = inputTokens' },
    { text: '                           + cacheReadTokens' },
    { text: '                           + cacheWriteTokens' },
    { text: 'gen_ai.usage.output_tokens = outputTokens' },
    { kind: 'blank' },
    { kind: 'comment', text: `# ${note}` },
    { text: 'dsh.usage.uncached_input_tokens' },
    { text: 'dsh.usage.cache_read_tokens' },
    { text: 'dsh.usage.cache_write_tokens' },
    { text: 'dsh.usage.reasoning_tokens' },
  ],
});

const logsSQL = (title: string): CodeBlock => ({
  title,
  lines: [
    { text: 'SELECT session_id, event_type, turn, step, body' },
    { text: 'FROM dsh_logs' },
    { text: "WHERE session_id = '...' AND event_type = 'tool/result'" },
    { text: 'ORDER BY timestamp;' },
  ],
});

const tma1Yaml: CodeBlock = {
  title: 'cordis.patch.yml',
  lines: [{ text: 'endpoint: http://localhost:14318/v1/otlp' }],
};

const dockerRun = (title: string): CodeBlock => ({
  title,
  lines: [
    { text: 'docker run -p 127.0.0.1:4000-4003:4000-4003 \\' },
    { text: '  -v "$(pwd)/greptimedb_data:/greptimedb_data" \\' },
    { text: '  --name greptime --rm greptime/greptimedb:v1.2.0-beta.2 standalone start \\' },
    { text: '  --http-addr 0.0.0.0:4000 --rpc-bind-addr 0.0.0.0:4001 \\' },
    { text: '  --mysql-addr 0.0.0.0:4002 --postgres-addr 0.0.0.0:4003' },
  ],
});

const patchYaml = (title: string): CodeBlock => ({
  title,
  lines: [
    { text: '- id: greptimedb-otel' },
    { text: "  name: '@tma1-ai/dsh-plugin-greptimedb'" },
    { text: '  config:' },
    { text: '    endpoint: https://<host>/v1/otlp' },
    { text: '    database: <dbname>' },
    { text: '    username: <user>' },
    { text: '    password: <password>' },
  ],
});

export const en: ProductCopy = {
  lang: 'en',
  title: 'DSH OTel — DeepSeek Harness telemetry as OpenTelemetry',
  description:
    'A DeepSeek Harness plugin that exports every turn, model call, and tool execution as OpenTelemetry traces, metrics, and logs. No collector, no sidecar, no fork of DSH.',
  og: { image: '/products/dsh-otel/overview.webp', alt: 'DSH OTel Grafana overview dashboard' },
  nav: [
    { href: '#features', label: 'Signals' },
    { href: '#config', label: 'Config' },
    { href: '#privacy', label: 'What leaves' },
    { href: '#limits', label: 'Limitations' },
  ],

  hero: {
    eyebrow: 'DSH OTel',
    h1_1: 'DeepSeek Harness telemetry, ',
    h1_2: 'as plain OpenTelemetry.',
    subtitle: `No collector. No sidecar. No fork of DSH. It installs as an ordinary <a href="${DSH}">DeepSeek Harness</a> plugin, and every turn, model call, and tool execution becomes a row in <a href="${GREPTIMEDB}">GreptimeDB</a> you can query.`,
    badges: [
      { label: 'npm', href: NPM },
      { label: 'CI', href: `${REPO}/actions/workflows/ci.yml` },
      { label: 'node ≥ 22.19', href: 'https://nodejs.org' },
      { label: 'Apache-2.0', href: `${REPO}/blob/main/LICENSE` },
    ],
  },

  install: {
    label: 'INSTALL',
    cmd: 'dsh plugin --profile headless add @tma1-ai/dsh-plugin-greptimedb',
    note: 'The package ships a bundle patch, so that one command wires it into the profile. <code>dsh plugin</code> forwards to whichever <code>pnpm</code> is on your PATH, and a dsh profile directory is its own pnpm workspace root — pnpm 9 refuses to install there and ignores the linker settings dsh writes, so use pnpm 10 or newer. The defaults already point at a local GreptimeDB.',
    more_summary: 'Point it at your own database, or start a local one',
    more: [
      patchYaml('$DSH_HOME/profiles/<name>/cordis.patch.yml'),
      dockerRun('local greptimedb'),
    ],
  },

  hook: {
    block: joinSQL('Slowest tool calls, with the model that requested them.'),
    caption:
      'Chat spans and tool spans share a trace and a <code>dsh.step</code>, so correlating them is a plain SQL join. Every timestamp comes from the session event it belongs to, not from a clock read while the event is being handled.',
  },

  highlights: [
    {
      title: 'Three signals, one plugin',
      desc: 'Traces, metrics, and logs. <code>signals</code> takes any subset — a disabled signal builds no exporter at all.',
    },
    {
      title: 'Nothing leaves by default',
      desc: 'The default <code>content: none</code> exports structure and accounting only. No prompts, no messages, no tool arguments, no tool results.',
    },
    {
      title: 'Fails at load, not at export',
      desc: 'Bad configuration fails when the plugin loads, with the offending field named — not silently at the first export.',
    },
  ],

  features: {
    id: 'features',
    kicker: 'Signals',
    title: 'What lands in each signal',
    desc: 'Traces for shape, metrics for long retention and sampling-proof percentiles, logs for the raw session events.',
    rows: [
      {
        num: '[01]',
        title: 'Turn, chat, tool',
        desc: 'Turn spans are roots. Chat and tool spans hang off them as siblings, correlated by <code>dsh.step</code>.',
        media: { kind: 'code', block: spanTree('span tree') },
      },
      {
        num: '[02]',
        title: 'Every chat span has a defined end',
        desc: 'Four paths close a chat span, including the crash case. None of them leaves a span dangling at an arbitrary time.',
        media: {
          kind: 'table',
          head: ['Situation', 'End time and status'],
          rows: [
            ['Model responded', '<code>assistant/message</code> · OK'],
            ['Stream interrupted', '<code>assistant/message</code> · OK, plus <code>dsh.response.interrupted</code>'],
            ['Request failed', 'that step’s <code>step/end</code> · ERROR, with the error type'],
            ['No end event (crash, teardown)', 'last event seen · UNSET, plus <code>dsh.span.unclosed</code>'],
          ],
        },
      },
      {
        num: '[03]',
        title: 'Token accounting',
        desc: 'DSH’s counts are disjoint: <code>inputTokens</code> is uncached input alone, cache reads and writes are separate fields. <code>gen_ai.usage.input_tokens</code> is the billed total, so the plugin sums them. Output includes reasoning tokens.',
        media: { kind: 'code', block: tokenMath('token attributes', 'the breakdown stays queryable') },
      },
      {
        num: '[04]',
        title: 'Five Grafana dashboards',
        desc: 'They ship in <code>grafana/</code> with a compose stack that brings up GreptimeDB and Grafana together. Every panel query is checked against a live database by <code>node grafana/verify.mjs</code>.',
        media: {
          kind: 'image',
          src: `${SHOT}/overview.webp`,
          w: 1185,
          h: 1174,
          alt: 'DSH OTel overview dashboard — turns, model calls, billed tokens, cache share, latency',
          chrome: 'localhost:3000 · overview',
        },
      },
      {
        num: '[05]',
        title: 'One turn, span by span',
        desc: 'Every table links onward: a trace id opens that turn’s waterfall, a session id jumps between the trace and log views.',
        media: {
          kind: 'image',
          src: `${SHOT}/trace-explorer.webp`,
          w: 1185,
          h: 1592,
          alt: 'DSH OTel trace explorer dashboard',
          chrome: 'localhost:3000 · trace explorer',
        },
      },
      {
        num: '[06]',
        title: 'One row per session event',
        desc: 'Four attributes become real columns through <code>X-Greptime-Log-Extract-Keys</code>. <code>assistant/chunk</code> is never exported — the assembled <code>assistant/message</code> carries the same content.',
        media: { kind: 'code', block: logsSQL('greptimedb · sql') },
      },
    ],
  },

  sections: [
    {
      kind: 'table',
      kicker: 'Metrics',
      title: 'Instruments',
      desc: 'The same activity as the traces, through PromQL — for longer retention and percentiles that survive sampling.',
      head: ['Instrument', 'Type', 'Dimensions'],
      rows: [
        ['<code>gen_ai.client.token.usage</code>', 'Histogram', '<code>gen_ai.token.type</code> (<code>input</code>/<code>output</code> only), model, provider'],
        ['<code>gen_ai.client.operation.duration</code>', 'Histogram', '<code>gen_ai.operation.name</code>, model, provider'],
        ['<code>gen_ai.invoke_agent.duration</code>', 'Histogram', '<code>gen_ai.operation.name</code>'],
        ['<code>gen_ai.execute_tool.duration</code>', 'Histogram', '<code>gen_ai.operation.name</code>, <code>gen_ai.tool.name</code>'],
        ['<code>dsh.token.detail</code>', 'Histogram', '<code>dsh.token.detail_kind</code> (<code>cache_read</code>/<code>cache_write</code>/<code>reasoning</code>)'],
        ['<code>dsh.tool.invocations</code>', 'Counter', '<code>gen_ai.tool.name</code>, <code>dsh.tool.outcome</code>'],
        ['<code>dsh.turns</code> / <code>dsh.steps</code>', 'Counter', '—'],
      ],
    },
    {
      kind: 'table',
      id: 'config',
      kicker: 'Configuration',
      title: 'The keys you actually set',
      desc: 'A profile patch replaces the row’s whole <code>config</code> instead of merging into it, so restate every field you want to keep.',
      head: ['Key', 'Default', 'Notes'],
      rows: [
        ['<code>endpoint</code>', '<em>required</em>', 'OTLP <strong>base</strong> URL. The plugin appends each signal’s <code>/v1/{traces,metrics,logs}</code> suffix; a per-signal path is rejected at load.'],
        ['<code>database</code>', '<code>public</code>', 'Sent as <code>X-Greptime-DB-Name</code>.'],
        ['<code>username</code> / <code>password</code>', '<em>none</em>', 'Basic auth. Both or neither.'],
        ['<code>signals</code>', 'all three', 'Any subset of <code>traces</code>, <code>metrics</code>, <code>logs</code>.'],
        ['<code>content</code>', '<code>none</code>', 'How much payload may leave the process.'],
        ['<code>ttl</code>', '<code>180d</code>', 'Retention for the log and trace tables this plugin creates, sent as <code>x-greptime-hints</code>. Also accepts <code>forever</code>. An existing table keeps its own until <code>ALTER TABLE</code>.'],
      ],
      note: 'Batching, timeouts, service name, and table overrides have sensible defaults; the full table is in the <a href="https://github.com/tma1-ai/dsh-otel#configuration">README</a>.',
    },
    {
      kind: 'table',
      id: 'privacy',
      kicker: 'What leaves the machine',
      title: '<code>content</code> decides this',
      desc: 'The default withholds all payloads. Raise it deliberately, per profile.',
      head: ['Mode', 'Exported'],
      rows: [
        ['<code>none</code> <em>(default)</em>', 'Structure and accounting: event types, turn and step numbers, token counts, tool names, durations, outcomes, error <code>name</code> and <code>code</code>.'],
        ['<code>full</code>', 'Adds user and assistant message content, tool arguments, tool results.'],
        ['<code>full+prompt</code>', 'Adds <code>request/header</code>: the complete system prompt and every tool schema.'],
      ],
      note: 'Three things never leave in any mode: a tool’s private <code>meta</code> payload, the internal <code>error.message</code> of a failed turn, and the message and stack of a failed request. The projection is a positive allowlist, so an event type the plugin does not know — including one a future DSH plugin declares — exports its identity and nothing else.',
    },
    {
      kind: 'panel',
      kicker: 'With TMA1',
      title: 'Point it at TMA1 instead',
      desc: 'TMA1 proxies OTLP into a GreptimeDB it manages. Change one line and DSH shows up in its OTel GenAI view.',
      panel_title: 'The flow tables already line up',
      panel_body:
        'TMA1’s <code>tma1_token_usage_1m</code>, <code>cost_1m</code>, <code>latency_1m</code>, and <code>status_1m</code> flow tables derive from <code>span_attributes.gen_ai.*</code>, which this plugin populates by convention. Nothing else to configure.',
      code: tma1Yaml,
    },
    {
      kind: 'cards',
      id: 'limits',
      kicker: 'Known limitations',
      title: 'Read these first',
      desc: '',
      cards: [
        {
          title: 'DSH is pre-release',
          desc: 'It renames and repackages freely before its first tagged release. The peer range is the exact version CI runs against (<code>0.1.1-rc.2</code>); a new DSH release needs a tested bump here.',
        },
        {
          title: 'The GenAI conventions are experimental',
          desc: 'Names come from <code>@opentelemetry/semantic-conventions/incubating</code> and move with it. Spans carry both <code>gen_ai.provider.name</code> and the deprecated <code>gen_ai.system</code>.',
        },
        {
          title: '<code>ttl</code> does not reach metric tables',
          desc: 'Metrics land on the metric engine, where retention is a property of the physical table. The hint reaches the logical table, which stores and displays it but never enforces it (<a href="https://github.com/GreptimeTeam/greptimedb/issues/8951">greptimedb#8951</a>). Set it yourself with <code>ALTER TABLE greptime_physical_table SET \'ttl\' = \'180d\'</code>.',
        },
        {
          title: 'Export is batched, shutdown is bounded',
          desc: 'There is no per-turn flush — export follows the batch processors’ cadence. Records still in flight when <code>shutdownTimeoutMillis</code> expires may be lost at exit.',
        },
        {
          title: 'Subagent sessions get their own trace',
          desc: 'They are not stitched into the parent’s.',
        },
      ],
    },
  ],

  footer: {
    tagline: `Apache-2.0. A plugin for <a href="${DSH}">DeepSeek Harness</a>, exporting to <a href="${GREPTIMEDB}">GreptimeDB</a>.`,
    links: [
      { href: REPO, label: 'GitHub' },
      { href: NPM, label: 'npm' },
      { href: `${REPO}/blob/main/LICENSE`, label: 'Apache-2.0' },
      { href: `${REPO}/blob/main/grafana/README.md`, label: 'Dashboards' },
    ],
  },
};

export const zh: ProductCopy = {
  ...en,
  lang: 'zh',
  title: 'DSH OTel — 把 DeepSeek Harness 的遥测变成 OpenTelemetry',
  description:
    '一个 DeepSeek Harness 插件，把每个 turn、模型调用和工具执行导出成 OpenTelemetry 的 traces、metrics 和 logs。不需要 collector，不需要 sidecar，不用 fork DSH。',
  nav: [
    { href: '#features', label: '三种信号' },
    { href: '#config', label: '配置' },
    { href: '#privacy', label: '哪些数据会出去' },
    { href: '#limits', label: '已知限制' },
  ],

  hero: {
    eyebrow: 'DSH OTel',
    h1_1: 'DeepSeek Harness 的遥测，',
    h1_2: '就是标准 OpenTelemetry。',
    subtitle: `不需要 collector，不需要 sidecar，不用 fork DSH。它就是一个普通的 <a href="${DSH}">DeepSeek Harness</a> 插件，装上之后每个 turn、每次模型调用、每次工具执行都变成 <a href="${GREPTIMEDB}">GreptimeDB</a> 里一行可查的数据。`,
    badges: [
      { label: 'npm', href: NPM },
      { label: 'CI', href: `${REPO}/actions/workflows/ci.yml` },
      { label: 'node ≥ 22.19', href: 'https://nodejs.org' },
      { label: 'Apache-2.0', href: `${REPO}/blob/main/LICENSE` },
    ],
  },

  install: {
    label: '安装',
    cmd: 'dsh plugin --profile headless add @tma1-ai/dsh-plugin-greptimedb',
    note: '包里自带 bundle patch，所以这一条命令就把它接进了 profile。<code>dsh plugin</code> 会转发给 PATH 上的 <code>pnpm</code>，而 dsh 的 profile 目录本身就是一个 pnpm workspace 根目录——pnpm 9 会拒绝在那里安装，并且忽略 dsh 写入的 linker 设置，所以请用 pnpm 10 及以上。默认配置已经指向本地的 GreptimeDB。',
    more_summary: '指向你自己的数据库，或者先起一个本地实例',
    more: [
      patchYaml('$DSH_HOME/profiles/<name>/cordis.patch.yml'),
      dockerRun('本地 greptimedb'),
    ],
  },

  hook: {
    block: joinSQL('最慢的工具调用，以及是哪个模型发起的。'),
    caption:
      'chat span 和 tool span 共享同一个 trace 和同一个 <code>dsh.step</code>，所以关联它们就是一次普通的 SQL join。每个时间戳都取自它对应的那条 session 事件，而不是处理事件时读的时钟。',
  },

  highlights: [
    {
      title: '三种信号，一个插件',
      desc: 'traces、metrics、logs。<code>signals</code> 接受任意子集——关掉的信号根本不会构建 exporter。',
    },
    {
      title: '默认什么内容都不出去',
      desc: '默认的 <code>content: none</code> 只导出结构和计数。没有 prompt，没有消息内容，没有工具参数，没有工具返回。',
    },
    {
      title: '配置错了在加载时就报',
      desc: '配置有问题时插件加载阶段直接失败，并指出是哪个字段——而不是等到第一次导出才悄悄出错。',
    },
  ],

  features: {
    id: 'features',
    kicker: '三种信号',
    title: '每种信号里各有什么',
    desc: 'traces 看结构，metrics 用于长期留存和不受采样影响的分位数，logs 保留原始 session 事件。',
    rows: [
      {
        num: '[01]',
        title: 'turn、chat、tool',
        desc: 'turn span 是根节点。chat span 和 tool span 作为兄弟节点挂在它下面，通过 <code>dsh.step</code> 关联。',
        media: { kind: 'code', block: spanTree('span 树') },
      },
      {
        num: '[02]',
        title: '每个 chat span 都有确定的结束时间',
        desc: '四条路径关闭一个 chat span，包括崩溃这种情况。没有一条会让 span 悬在一个随意的时间点上。',
        media: {
          kind: 'table',
          head: ['情况', '结束时间与状态'],
          rows: [
            ['模型正常返回', '<code>assistant/message</code> · OK'],
            ['流被打断', '<code>assistant/message</code> · OK，附带 <code>dsh.response.interrupted</code>'],
            ['请求失败', '该 step 的 <code>step/end</code> · ERROR，带错误类型'],
            ['没有结束事件（崩溃、进程退出）', '最后见到的事件 · UNSET，附带 <code>dsh.span.unclosed</code>'],
          ],
        },
      },
      {
        num: '[03]',
        title: 'Token 口径',
        desc: 'DSH 的计数是不相交的：<code>inputTokens</code> 只算未命中缓存的输入，缓存读和缓存写是独立字段。<code>gen_ai.usage.input_tokens</code> 是计费总量，所以插件把它们加起来。输出侧包含 reasoning token。',
        media: { kind: 'code', block: tokenMath('token 属性', '拆分口径仍然可查') },
      },
      {
        num: '[04]',
        title: '五个 Grafana 仪表盘',
        desc: '仪表盘放在 <code>grafana/</code>，配套一个把 GreptimeDB 和 Grafana 一起拉起来的 compose stack。每个面板的查询都由 <code>node grafana/verify.mjs</code> 对着真实数据库校验过。',
        media: {
          kind: 'image',
          src: `${SHOT}/overview.webp`,
          w: 1185,
          h: 1174,
          alt: 'DSH OTel overview 仪表盘：turn 数、模型调用、计费 token、缓存命中率、延迟',
          chrome: 'localhost:3000 · overview',
        },
      },
      {
        num: '[05]',
        title: '一个 turn，逐个 span 看',
        desc: '每张表都能往下跳：trace id 打开那个 turn 的 waterfall，session id 在 trace 视图和日志视图之间切换。',
        media: {
          kind: 'image',
          src: `${SHOT}/trace-explorer.webp`,
          w: 1185,
          h: 1592,
          alt: 'DSH OTel trace explorer 仪表盘',
          chrome: 'localhost:3000 · trace explorer',
        },
      },
      {
        num: '[06]',
        title: '一条 session 事件一行',
        desc: '四个属性通过 <code>X-Greptime-Log-Extract-Keys</code> 变成真正的列。<code>assistant/chunk</code> 永远不导出——拼装好的 <code>assistant/message</code> 已经带着同样的内容。',
        media: { kind: 'code', block: logsSQL('greptimedb · sql') },
      },
    ],
  },

  sections: [
    {
      kind: 'table',
      kicker: 'Metrics',
      title: '指标 instrument',
      desc: '和 traces 同一批活动，换成 PromQL 看——用于更长的留存，以及不受采样影响的分位数。',
      head: ['Instrument', '类型', '维度'],
      rows: [
        ['<code>gen_ai.client.token.usage</code>', 'Histogram', '<code>gen_ai.token.type</code>（只有 <code>input</code>/<code>output</code>）、model、provider'],
        ['<code>gen_ai.client.operation.duration</code>', 'Histogram', '<code>gen_ai.operation.name</code>、model、provider'],
        ['<code>gen_ai.invoke_agent.duration</code>', 'Histogram', '<code>gen_ai.operation.name</code>'],
        ['<code>gen_ai.execute_tool.duration</code>', 'Histogram', '<code>gen_ai.operation.name</code>、<code>gen_ai.tool.name</code>'],
        ['<code>dsh.token.detail</code>', 'Histogram', '<code>dsh.token.detail_kind</code>（<code>cache_read</code>/<code>cache_write</code>/<code>reasoning</code>）'],
        ['<code>dsh.tool.invocations</code>', 'Counter', '<code>gen_ai.tool.name</code>、<code>dsh.tool.outcome</code>'],
        ['<code>dsh.turns</code> / <code>dsh.steps</code>', 'Counter', '—'],
      ],
    },
    {
      kind: 'table',
      id: 'config',
      kicker: '配置',
      title: '实际会改的几个键',
      desc: 'profile patch 会整个替换掉这一行的 <code>config</code>，而不是合并进去，所以想保留的字段都要重新写一遍。',
      head: ['键', '默认值', '说明'],
      rows: [
        ['<code>endpoint</code>', '<em>必填</em>', 'OTLP <strong>基础</strong> URL。插件会自己拼上每种信号的 <code>/v1/{traces,metrics,logs}</code> 后缀；写成单信号路径会在加载时被拒绝。'],
        ['<code>database</code>', '<code>public</code>', '作为 <code>X-Greptime-DB-Name</code> 发送。'],
        ['<code>username</code> / <code>password</code>', '<em>无</em>', 'Basic auth。要么都填，要么都不填。'],
        ['<code>signals</code>', '三种全开', '<code>traces</code>、<code>metrics</code>、<code>logs</code> 的任意子集。'],
        ['<code>content</code>', '<code>none</code>', '允许多少 payload 离开进程。'],
        ['<code>ttl</code>', '<code>180d</code>', '插件创建的日志表和 trace 表的留存时间，通过 <code>x-greptime-hints</code> 发送，也接受 <code>forever</code>。已存在的表要 <code>ALTER TABLE</code> 才会变。'],
      ],
      note: '批量、超时、service name、目标表名都有合理默认值；完整表格见 <a href="https://github.com/tma1-ai/dsh-otel#configuration">README</a>。',
    },
    {
      kind: 'table',
      id: 'privacy',
      kicker: '哪些数据会离开这台机器',
      title: '由 <code>content</code> 决定',
      desc: '默认不导出任何 payload。要放开就按 profile 显式放开。',
      head: ['模式', '导出内容'],
      rows: [
        ['<code>none</code> <em>（默认）</em>', '结构和计数：事件类型、turn 与 step 编号、token 数、工具名、耗时、结果，以及错误的 <code>name</code> 和 <code>code</code>。'],
        ['<code>full</code>', '增加用户和助手的消息内容、工具参数、工具返回。'],
        ['<code>full+prompt</code>', '再增加 <code>request/header</code>：完整的 system prompt 和每个工具的 schema。'],
      ],
      note: '有三样东西在任何模式下都不会出去：工具私有的 <code>meta</code> payload、失败 turn 的内部 <code>error.message</code>，以及失败请求的 message 和 stack。这个投影是白名单式的，所以插件不认识的事件类型——包括未来某个 DSH 插件新声明的——只会导出它的身份，别的什么都没有。',
    },
    {
      kind: 'panel',
      kicker: '配合 TMA1',
      title: '也可以直接指向 TMA1',
      desc: 'TMA1 会把 OTLP 代理进它自己管理的 GreptimeDB。改一行配置，DSH 就出现在它的 OTel GenAI 视图里。',
      panel_title: 'flow 表本来就对得上',
      panel_body:
        'TMA1 的 <code>tma1_token_usage_1m</code>、<code>cost_1m</code>、<code>latency_1m</code>、<code>status_1m</code> 这几张 flow 表都是从 <code>span_attributes.gen_ai.*</code> 推导出来的，而这个插件按约定就会填这些字段。除此之外不用配任何东西。',
      code: tma1Yaml,
    },
    {
      kind: 'cards',
      id: 'limits',
      kicker: '已知限制',
      title: '接进正式环境前先看这些',
      desc: '',
      cards: [
        {
          title: 'DSH 还没正式发布',
          desc: '在第一个 tag 之前它会随意改名和重新打包。peer 版本范围就是 CI 实际跑的那个版本（<code>0.1.1-rc.2</code>）；DSH 发新版需要在这里做一次经过测试的 bump。',
        },
        {
          title: 'GenAI 语义约定还是实验性的',
          desc: '字段名来自 <code>@opentelemetry/semantic-conventions/incubating</code>，会跟着它变。span 上同时带 <code>gen_ai.provider.name</code> 和已废弃的 <code>gen_ai.system</code>。',
        },
        {
          title: '<code>ttl</code> 到不了指标表',
          desc: '指标落在 metric engine 上，那里的留存是物理表的属性。hint 只到逻辑表，逻辑表会存下来并显示，但不会执行（<a href="https://github.com/GreptimeTeam/greptimedb/issues/8951">greptimedb#8951</a>）。需要自己执行 <code>ALTER TABLE greptime_physical_table SET \'ttl\' = \'180d\'</code>。',
        },
        {
          title: '导出是批量的，退出有时限',
          desc: '没有按 turn 的 flush——导出跟着 batch processor 的节奏走。<code>shutdownTimeoutMillis</code> 到点时还在途中的记录可能在退出时丢失。',
        },
        {
          title: '子 agent 的 session 自成一条 trace',
          desc: '不会缝进父 trace 里。',
        },
      ],
    },
  ],

  footer: {
    tagline: `Apache-2.0。<a href="${DSH}">DeepSeek Harness</a> 的插件，导出到 <a href="${GREPTIMEDB}">GreptimeDB</a>。`,
    links: [
      { href: REPO, label: 'GitHub' },
      { href: NPM, label: 'npm' },
      { href: `${REPO}/blob/main/LICENSE`, label: 'Apache-2.0' },
      { href: `${REPO}/blob/main/grafana/README.md`, label: '仪表盘' },
    ],
  },
};

export const es: ProductCopy = {
  ...en,
  lang: 'es',
  title: 'DSH OTel — telemetría de DeepSeek Harness como OpenTelemetry',
  description:
    'Un plugin de DeepSeek Harness que exporta cada turno, llamada al modelo y ejecución de herramienta como traces, métricas y logs de OpenTelemetry. Sin collector, sin sidecar, sin forkear DSH.',
  nav: [
    { href: '#features', label: 'Señales' },
    { href: '#config', label: 'Configuración' },
    { href: '#privacy', label: 'Qué sale' },
    { href: '#limits', label: 'Limitaciones' },
  ],

  hero: {
    eyebrow: 'DSH OTel',
    h1_1: 'Telemetría de DeepSeek Harness, ',
    h1_2: 'como OpenTelemetry puro.',
    subtitle: `Sin collector. Sin sidecar. Sin forkear DSH. Se instala como un plugin común de <a href="${DSH}">DeepSeek Harness</a>, y cada turno, llamada al modelo y ejecución de herramienta pasa a ser una fila consultable en <a href="${GREPTIMEDB}">GreptimeDB</a>.`,
    badges: [
      { label: 'npm', href: NPM },
      { label: 'CI', href: `${REPO}/actions/workflows/ci.yml` },
      { label: 'node ≥ 22.19', href: 'https://nodejs.org' },
      { label: 'Apache-2.0', href: `${REPO}/blob/main/LICENSE` },
    ],
  },

  install: {
    label: 'INSTALACIÓN',
    cmd: 'dsh plugin --profile headless add @tma1-ai/dsh-plugin-greptimedb',
    note: 'El paquete trae un bundle patch, así que ese único comando lo conecta al profile. <code>dsh plugin</code> delega en el <code>pnpm</code> que esté en tu PATH, y un directorio de profile de dsh es su propio workspace root de pnpm — pnpm 9 se niega a instalar ahí e ignora la configuración de linker que escribe dsh, así que usá pnpm 10 o superior. Los valores por defecto ya apuntan a un GreptimeDB local.',
    more_summary: 'Apuntalo a tu propia base de datos, o levantá una local',
    more: [
      patchYaml('$DSH_HOME/profiles/<name>/cordis.patch.yml'),
      dockerRun('greptimedb local'),
    ],
  },

  hook: {
    block: joinSQL('Las llamadas a herramientas más lentas, con el modelo que las pidió.'),
    caption:
      'Los spans de chat y los de herramienta comparten trace y <code>dsh.step</code>, así que correlacionarlos es un join de SQL común. Cada timestamp viene del evento de sesión al que pertenece, no de leer el reloj mientras se procesa el evento.',
  },

  highlights: [
    {
      title: 'Tres señales, un plugin',
      desc: 'Traces, métricas y logs. <code>signals</code> acepta cualquier subconjunto — una señal desactivada no construye exporter alguno.',
    },
    {
      title: 'Por defecto no sale nada',
      desc: 'El valor por defecto <code>content: none</code> exporta solo estructura y contabilidad. Sin prompts, sin mensajes, sin argumentos de herramientas, sin resultados.',
    },
    {
      title: 'Falla al cargar, no al exportar',
      desc: 'Una configuración inválida falla cuando el plugin carga, nombrando el campo culpable — no en silencio durante la primera exportación.',
    },
  ],

  features: {
    id: 'features',
    kicker: 'Señales',
    title: 'Qué llega en cada señal',
    desc: 'Traces para la forma, métricas para retención larga y percentiles a prueba de sampleo, logs para los eventos de sesión crudos.',
    rows: [
      {
        num: '[01]',
        title: 'Turno, chat, herramienta',
        desc: 'Los spans de turno son raíces. Los de chat y herramienta cuelgan de ellos como hermanos, correlacionados por <code>dsh.step</code>.',
        media: { kind: 'code', block: spanTree('árbol de spans') },
      },
      {
        num: '[02]',
        title: 'Todo span de chat tiene un fin definido',
        desc: 'Cuatro caminos cierran un span de chat, incluido el de caída. Ninguno deja un span colgado en un instante arbitrario.',
        media: {
          kind: 'table',
          head: ['Situación', 'Fin y estado'],
          rows: [
            ['El modelo respondió', '<code>assistant/message</code> · OK'],
            ['Stream interrumpido', '<code>assistant/message</code> · OK, más <code>dsh.response.interrupted</code>'],
            ['La petición falló', 'el <code>step/end</code> de ese step · ERROR, con el tipo de error'],
            ['Sin evento de cierre (caída, apagado)', 'último evento visto · UNSET, más <code>dsh.span.unclosed</code>'],
          ],
        },
      },
      {
        num: '[03]',
        title: 'Contabilidad de tokens',
        desc: 'Los conteos de DSH son disjuntos: <code>inputTokens</code> es solo entrada sin caché, y las lecturas y escrituras de caché son campos aparte. <code>gen_ai.usage.input_tokens</code> es el total facturado, así que el plugin los suma. La salida incluye los tokens de razonamiento.',
        media: { kind: 'code', block: tokenMath('atributos de tokens', 'el desglose sigue siendo consultable') },
      },
      {
        num: '[04]',
        title: 'Cinco dashboards de Grafana',
        desc: 'Vienen en <code>grafana/</code> junto a un stack de compose que levanta GreptimeDB y Grafana a la vez. Cada consulta de panel se verifica contra una base real con <code>node grafana/verify.mjs</code>.',
        media: {
          kind: 'image',
          src: `${SHOT}/overview.webp`,
          w: 1185,
          h: 1174,
          alt: 'Dashboard overview de DSH OTel: turnos, llamadas al modelo, tokens facturados, caché, latencia',
          chrome: 'localhost:3000 · overview',
        },
      },
      {
        num: '[05]',
        title: 'Un turno, span por span',
        desc: 'Cada tabla enlaza hacia adelante: un trace id abre el waterfall de ese turno, un session id salta entre la vista de traces y la de logs.',
        media: {
          kind: 'image',
          src: `${SHOT}/trace-explorer.webp`,
          w: 1185,
          h: 1592,
          alt: 'Dashboard trace explorer de DSH OTel',
          chrome: 'localhost:3000 · trace explorer',
        },
      },
      {
        num: '[06]',
        title: 'Una fila por evento de sesión',
        desc: 'Cuatro atributos se vuelven columnas reales vía <code>X-Greptime-Log-Extract-Keys</code>. <code>assistant/chunk</code> nunca se exporta — el <code>assistant/message</code> ya ensamblado trae el mismo contenido.',
        media: { kind: 'code', block: logsSQL('greptimedb · sql') },
      },
    ],
  },

  sections: [
    {
      kind: 'table',
      kicker: 'Métricas',
      title: 'Instrumentos',
      desc: 'La misma actividad que los traces, vista con PromQL — para retención más larga y percentiles que sobreviven al sampleo.',
      head: ['Instrumento', 'Tipo', 'Dimensiones'],
      rows: [
        ['<code>gen_ai.client.token.usage</code>', 'Histogram', '<code>gen_ai.token.type</code> (solo <code>input</code>/<code>output</code>), model, provider'],
        ['<code>gen_ai.client.operation.duration</code>', 'Histogram', '<code>gen_ai.operation.name</code>, model, provider'],
        ['<code>gen_ai.invoke_agent.duration</code>', 'Histogram', '<code>gen_ai.operation.name</code>'],
        ['<code>gen_ai.execute_tool.duration</code>', 'Histogram', '<code>gen_ai.operation.name</code>, <code>gen_ai.tool.name</code>'],
        ['<code>dsh.token.detail</code>', 'Histogram', '<code>dsh.token.detail_kind</code> (<code>cache_read</code>/<code>cache_write</code>/<code>reasoning</code>)'],
        ['<code>dsh.tool.invocations</code>', 'Counter', '<code>gen_ai.tool.name</code>, <code>dsh.tool.outcome</code>'],
        ['<code>dsh.turns</code> / <code>dsh.steps</code>', 'Counter', '—'],
      ],
    },
    {
      kind: 'table',
      id: 'config',
      kicker: 'Configuración',
      title: 'Las claves que realmente vas a tocar',
      desc: 'Un patch de profile reemplaza todo el <code>config</code> de esa fila en vez de fusionarse con él, así que reescribí cada campo que quieras conservar.',
      head: ['Clave', 'Por defecto', 'Notas'],
      rows: [
        ['<code>endpoint</code>', '<em>requerido</em>', 'URL <strong>base</strong> de OTLP. El plugin agrega el sufijo <code>/v1/{traces,metrics,logs}</code> de cada señal; una ruta por señal se rechaza al cargar.'],
        ['<code>database</code>', '<code>public</code>', 'Se envía como <code>X-Greptime-DB-Name</code>.'],
        ['<code>username</code> / <code>password</code>', '<em>ninguno</em>', 'Basic auth. Los dos o ninguno.'],
        ['<code>signals</code>', 'las tres', 'Cualquier subconjunto de <code>traces</code>, <code>metrics</code>, <code>logs</code>.'],
        ['<code>content</code>', '<code>none</code>', 'Cuánto payload puede salir del proceso.'],
        ['<code>ttl</code>', '<code>180d</code>', 'Retención de las tablas de logs y traces que crea el plugin, enviada como <code>x-greptime-hints</code>. También acepta <code>forever</code>. Una tabla ya existente conserva la suya hasta un <code>ALTER TABLE</code>.'],
      ],
      note: 'Batching, timeouts, nombre de servicio y overrides de tabla tienen valores razonables; la tabla completa está en el <a href="https://github.com/tma1-ai/dsh-otel#configuration">README</a>.',
    },
    {
      kind: 'table',
      id: 'privacy',
      kicker: 'Qué sale de la máquina',
      title: 'Lo decide <code>content</code>',
      desc: 'El valor por defecto retiene todos los payloads. Subilo a propósito, por profile.',
      head: ['Modo', 'Se exporta'],
      rows: [
        ['<code>none</code> <em>(por defecto)</em>', 'Estructura y contabilidad: tipos de evento, números de turno y step, conteos de tokens, nombres de herramientas, duraciones, resultados, <code>name</code> y <code>code</code> del error.'],
        ['<code>full</code>', 'Suma el contenido de los mensajes de usuario y asistente, argumentos de herramientas y resultados.'],
        ['<code>full+prompt</code>', 'Suma <code>request/header</code>: el system prompt completo y el schema de cada herramienta.'],
      ],
      note: 'Tres cosas no salen en ningún modo: el payload privado <code>meta</code> de una herramienta, el <code>error.message</code> interno de un turno fallido, y el mensaje y stack de una petición fallida. La proyección es una allowlist positiva, así que un tipo de evento que el plugin no conoce — incluido uno que declare un futuro plugin de DSH — exporta su identidad y nada más.',
    },
    {
      kind: 'panel',
      kicker: 'Con TMA1',
      title: 'Apuntalo a TMA1',
      desc: 'TMA1 hace de proxy OTLP hacia un GreptimeDB que él mismo administra. Cambiás una línea y DSH aparece en su vista OTel GenAI.',
      panel_title: 'Las flow tables ya coinciden',
      panel_body:
        'Las flow tables <code>tma1_token_usage_1m</code>, <code>cost_1m</code>, <code>latency_1m</code> y <code>status_1m</code> de TMA1 derivan de <code>span_attributes.gen_ai.*</code>, que este plugin completa por convención. No hay nada más que configurar.',
      code: tma1Yaml,
    },
    {
      kind: 'cards',
      id: 'limits',
      kicker: 'Limitaciones conocidas',
      title: 'Leé esto antes',
      desc: '',
      cards: [
        {
          title: 'DSH es pre-release',
          desc: 'Renombra y reempaqueta libremente hasta su primer tag. El rango de peer es la versión exacta contra la que corre CI (<code>0.1.1-rc.2</code>); una nueva release de DSH necesita un bump probado acá.',
        },
        {
          title: 'Las convenciones GenAI son experimentales',
          desc: 'Los nombres vienen de <code>@opentelemetry/semantic-conventions/incubating</code> y se mueven con él. Los spans llevan tanto <code>gen_ai.provider.name</code> como el obsoleto <code>gen_ai.system</code>.',
        },
        {
          title: '<code>ttl</code> no llega a las tablas de métricas',
          desc: 'Las métricas caen en el metric engine, donde la retención es una propiedad de la tabla física. El hint llega a la tabla lógica, que lo guarda y lo muestra pero nunca lo aplica (<a href="https://github.com/GreptimeTeam/greptimedb/issues/8951">greptimedb#8951</a>). Ponelo vos con <code>ALTER TABLE greptime_physical_table SET \'ttl\' = \'180d\'</code>.',
        },
        {
          title: 'La exportación es por lotes y el apagado es acotado',
          desc: 'No hay flush por turno — la exportación sigue la cadencia de los batch processors. Los registros en vuelo cuando expira <code>shutdownTimeoutMillis</code> pueden perderse al salir.',
        },
        {
          title: 'Las sesiones de subagente tienen su propio trace',
          desc: 'No se cosen dentro del trace padre.',
        },
      ],
    },
  ],

  footer: {
    tagline: `Apache-2.0. Un plugin para <a href="${DSH}">DeepSeek Harness</a> que exporta a <a href="${GREPTIMEDB}">GreptimeDB</a>.`,
    links: [
      { href: REPO, label: 'GitHub' },
      { href: NPM, label: 'npm' },
      { href: `${REPO}/blob/main/LICENSE`, label: 'Apache-2.0' },
      { href: `${REPO}/blob/main/grafana/README.md`, label: 'Dashboards' },
    ],
  },
};
