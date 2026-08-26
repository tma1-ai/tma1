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
    note: 'The package ships a bundle patch, so that one command is the whole install. Requires pnpm 10 or newer. The defaults already point at a local GreptimeDB.',
    more_summary: 'Point it at your own database, or start a local one',
    more: [
      patchYaml('$DSH_HOME/profiles/<name>/cordis.patch.yml'),
      dockerRun('local greptimedb'),
    ],
  },

  hook: {
    block: joinSQL('Slowest tool calls, with the model that requested them.'),
    caption:
      'Chat spans and tool spans share a trace and a <code>dsh.step</code>, so correlating them is a plain SQL join. Timestamps come from the session events themselves, not from when the plugin handled them.',
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
        desc: 'Turn spans are roots. Chat and tool spans hang off them as siblings, correlated by <code>dsh.step</code>. Every chat span gets a real end time, including the crash case.',
        media: { kind: 'code', block: spanTree('span tree') },
      },
      {
        num: '[02]',
        title: 'Token accounting',
        desc: 'DSH’s counts are disjoint: <code>inputTokens</code> is uncached input alone, cache reads and writes are separate fields. <code>gen_ai.usage.input_tokens</code> is the billed total, so the plugin sums them.',
        media: { kind: 'code', block: tokenMath('token attributes', 'the breakdown stays queryable') },
      },
      {
        num: '[03]',
        title: 'Five Grafana dashboards',
        desc: 'They ship in <code>grafana/</code> with a compose stack that brings up GreptimeDB and Grafana together. Every panel query is checked against a live database in CI.',
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
        num: '[04]',
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
        num: '[05]',
        title: 'One row per session event',
        desc: 'Session, event type, turn, and step are real columns, so filtering a session does not mean unpacking JSON.',
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
        ['<code>endpoint</code>', '<em>required</em>', 'OTLP <strong>base</strong> URL. The plugin appends the <code>/v1/{traces,metrics,logs}</code> suffix itself.'],
        ['<code>database</code>', '<code>public</code>', 'Sent as <code>X-Greptime-DB-Name</code>.'],
        ['<code>username</code> / <code>password</code>', '<em>none</em>', 'Basic auth. Both or neither.'],
        ['<code>signals</code>', 'all three', 'Any subset of <code>traces</code>, <code>metrics</code>, <code>logs</code>.'],
        ['<code>content</code>', '<code>none</code>', 'How much payload may leave the process.'],
        ['<code>ttl</code>', '<code>180d</code>', 'Retention for the tables the plugin creates. Also accepts <code>forever</code>.'],
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
      note: 'Whatever the mode, a tool’s private <code>meta</code> payload and the message and stack of a failed request never leave. The projection is an allowlist, so an event type the plugin does not know exports its identity and nothing else.',
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
      desc: 'The full list is in the <a href="https://github.com/tma1-ai/dsh-otel#known-limitations">README</a>.',
      cards: [
        {
          title: 'DSH is pre-release',
          desc: 'It renames and repackages freely before its first tagged release, so the peer range is pinned to the version CI runs against.',
        },
        {
          title: 'The GenAI conventions are experimental',
          desc: 'Attribute names come from <code>@opentelemetry/semantic-conventions/incubating</code> and move with it.',
        },
        {
          title: '<code>ttl</code> does not reach metric tables',
          desc: 'On the metric engine, retention belongs to the physical table, so set it there yourself (<a href="https://github.com/GreptimeTeam/greptimedb/issues/8951">greptimedb#8951</a>).',
        },
        {
          title: 'Export is batched',
          desc: 'There is no per-turn flush, and records still in flight at shutdown can be lost.',
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
    { href: '#privacy', label: '导出范围' },
    { href: '#limits', label: '已知限制' },
  ],

  hero: {
    eyebrow: 'DSH OTel',
    h1_1: 'DeepSeek Harness 的遥测，',
    h1_2: '就是标准 OpenTelemetry。',
    subtitle: `不需要 collector，不需要 sidecar，不用 fork DSH。它是一个标准的 <a href="${DSH}">DeepSeek Harness</a> 插件，安装后每个 turn、每次模型调用、每次工具执行都会成为 <a href="${GREPTIMEDB}">GreptimeDB</a> 中一行可查询的数据。`,
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
    note: '包内自带 bundle patch，一条命令即可完成安装。需要 pnpm 10 及以上版本。默认配置指向本地 GreptimeDB。',
    more_summary: '指向自有数据库，或先启动一个本地实例',
    more: [
      patchYaml('$DSH_HOME/profiles/<name>/cordis.patch.yml'),
      dockerRun('本地 greptimedb'),
    ],
  },

  hook: {
    block: joinSQL('最慢的工具调用，以及是哪个模型发起的。'),
    caption:
      'chat span 和 tool span 共享同一个 trace 和同一个 <code>dsh.step</code>，关联它们只需要一次普通的 SQL join。时间戳取自 session 事件本身，而非插件处理该事件的时刻。',
  },

  highlights: [
    {
      title: '三种信号，一个插件',
      desc: 'traces、metrics、logs。<code>signals</code> 接受任意子集，关闭的信号不会构建 exporter。',
    },
    {
      title: '默认不导出任何内容',
      desc: '默认的 <code>content: none</code> 只导出结构和计数，不含 prompt、消息内容、工具参数和工具返回。',
    },
    {
      title: '配置错误在加载阶段暴露',
      desc: '配置非法时插件在加载阶段失败并指出具体字段，而不是等到第一次导出才出错。',
    },
  ],

  features: {
    id: 'features',
    kicker: '三种信号',
    title: '每种信号包含什么',
    desc: 'traces 描述结构，metrics 用于长期留存和不受采样影响的分位数，logs 保留原始 session 事件。',
    rows: [
      {
        num: '[01]',
        title: 'turn、chat、tool',
        desc: 'turn span 是根节点，chat span 和 tool span 作为兄弟节点挂在其下，通过 <code>dsh.step</code> 关联。每个 chat span 都有确定的结束时间，崩溃时也是如此。',
        media: { kind: 'code', block: spanTree('span 树') },
      },
      {
        num: '[02]',
        title: 'Token 口径',
        desc: 'DSH 的计数互不重叠：<code>inputTokens</code> 只统计未命中缓存的输入，缓存读和缓存写是独立字段。<code>gen_ai.usage.input_tokens</code> 是计费总量，因此插件把三者相加。',
        media: { kind: 'code', block: tokenMath('token 属性', '拆分口径仍然可查') },
      },
      {
        num: '[03]',
        title: '五个 Grafana 仪表盘',
        desc: '仪表盘放在 <code>grafana/</code>，并附带一个同时启动 GreptimeDB 和 Grafana 的 compose stack。每个面板的查询都在 CI 中针对真实数据库校验。',
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
        num: '[04]',
        title: '一个 turn，逐个 span 看',
        desc: '每张表都可以继续下钻：trace id 打开该 turn 的 waterfall，session id 在 trace 视图和日志视图之间切换。',
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
        num: '[05]',
        title: '一条 session 事件一行',
        desc: 'session、事件类型、turn、step 都是独立的列，按 session 过滤无需解析 JSON。',
        media: { kind: 'code', block: logsSQL('greptimedb · sql') },
      },
    ],
  },

  sections: [
    {
      kind: 'table',
      kicker: 'Metrics',
      title: '指标 instrument',
      desc: '与 traces 覆盖同一批活动，改用 PromQL 查询，用于更长的留存和不受采样影响的分位数。',
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
      title: '常用的几个配置键',
      desc: 'profile patch 会整体替换该行的 <code>config</code> 而不是合并，需要保留的字段必须全部重写。',
      head: ['键', '默认值', '说明'],
      rows: [
        ['<code>endpoint</code>', '<em>必填</em>', 'OTLP <strong>基础</strong> URL，<code>/v1/{traces,metrics,logs}</code> 后缀由插件自动追加。'],
        ['<code>database</code>', '<code>public</code>', '作为 <code>X-Greptime-DB-Name</code> 发送。'],
        ['<code>username</code> / <code>password</code>', '<em>无</em>', 'Basic auth，两者同时提供或同时留空。'],
        ['<code>signals</code>', '全部三种', '<code>traces</code>、<code>metrics</code>、<code>logs</code> 的任意子集。'],
        ['<code>content</code>', '<code>none</code>', '允许多少 payload 离开进程。'],
        ['<code>ttl</code>', '<code>180d</code>', '插件创建的表的留存时间，也接受 <code>forever</code>。'],
      ],
      note: '批量、超时、service name 和目标表名都有默认值，完整列表见 <a href="https://github.com/tma1-ai/dsh-otel#configuration">README</a>。',
    },
    {
      kind: 'table',
      id: 'privacy',
      kicker: '哪些数据会离开本机',
      title: '由 <code>content</code> 决定',
      desc: '默认不导出任何 payload，需要放开时按 profile 显式配置。',
      head: ['模式', '导出内容'],
      rows: [
        ['<code>none</code> <em>（默认）</em>', '结构和计数：事件类型、turn 与 step 编号、token 数、工具名、耗时、结果，以及错误的 <code>name</code> 和 <code>code</code>。'],
        ['<code>full</code>', '增加用户和助手的消息内容、工具参数、工具返回。'],
        ['<code>full+prompt</code>', '再增加 <code>request/header</code>：完整的 system prompt 和每个工具的 schema。'],
      ],
      note: '无论哪种模式，工具私有的 <code>meta</code> payload、失败请求的 message 和 stack 都不会导出。投影按白名单进行，插件未知的事件类型只导出其标识。',
    },
    {
      kind: 'panel',
      kicker: '配合 TMA1',
      title: '也可以直接指向 TMA1',
      desc: 'TMA1 将 OTLP 代理到它自己管理的 GreptimeDB。改一行配置，DSH 就出现在它的 OTel GenAI 视图里。',
      panel_title: 'flow 表天然对齐',
      panel_body:
        'TMA1 的 <code>tma1_token_usage_1m</code>、<code>cost_1m</code>、<code>latency_1m</code>、<code>status_1m</code> 都由 <code>span_attributes.gen_ai.*</code> 推导，而这个插件按约定填充这些字段，不需要额外配置。',
      code: tma1Yaml,
    },
    {
      kind: 'cards',
      id: 'limits',
      kicker: '已知限制',
      title: '接入生产前先看这些',
      desc: '完整清单见 <a href="https://github.com/tma1-ai/dsh-otel#known-limitations">README</a>。',
      cards: [
        {
          title: 'DSH 还没正式发布',
          desc: '在第一个 tag 之前它会随意改名和重新打包，因此 peer 版本范围锁定在 CI 验证过的版本上。',
        },
        {
          title: 'GenAI 语义约定还是实验性的',
          desc: '属性名来自 <code>@opentelemetry/semantic-conventions/incubating</code>，会跟着它变。',
        },
        {
          title: '<code>ttl</code> 对指标表不生效',
          desc: 'metric engine 的留存是物理表的属性，需要在物理表上单独设置（<a href="https://github.com/GreptimeTeam/greptimedb/issues/8951">greptimedb#8951</a>）。',
        },
        {
          title: '导出是批量的',
          desc: '没有按 turn 的 flush，进程退出时仍在传输中的记录可能丢失。',
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
    note: 'El paquete trae un bundle patch, así que ese comando es toda la instalación. Requiere pnpm 10 o superior. Los valores por defecto ya apuntan a un GreptimeDB local.',
    more_summary: 'Apuntalo a tu propia base de datos, o levantá una local',
    more: [
      patchYaml('$DSH_HOME/profiles/<name>/cordis.patch.yml'),
      dockerRun('greptimedb local'),
    ],
  },

  hook: {
    block: joinSQL('Las llamadas a herramientas más lentas, con el modelo que las pidió.'),
    caption:
      'Los spans de chat y los de herramienta comparten trace y <code>dsh.step</code>, así que correlacionarlos es un join de SQL común. Los timestamps vienen de los propios eventos de sesión, no del momento en que el plugin los procesó.',
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
        desc: 'Los spans de turno son raíces. Los de chat y herramienta cuelgan de ellos como hermanos, correlacionados por <code>dsh.step</code>. Todo span de chat tiene un fin real, incluido el caso de caída.',
        media: { kind: 'code', block: spanTree('árbol de spans') },
      },
      {
        num: '[02]',
        title: 'Contabilidad de tokens',
        desc: 'Los conteos de DSH son disjuntos: <code>inputTokens</code> es solo entrada sin caché, y las lecturas y escrituras de caché son campos aparte. <code>gen_ai.usage.input_tokens</code> es el total facturado, así que el plugin los suma.',
        media: { kind: 'code', block: tokenMath('atributos de tokens', 'el desglose sigue siendo consultable') },
      },
      {
        num: '[03]',
        title: 'Cinco dashboards de Grafana',
        desc: 'Vienen en <code>grafana/</code> junto a un stack de compose que levanta GreptimeDB y Grafana a la vez. Cada consulta de panel se verifica contra una base real en CI.',
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
        num: '[04]',
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
        num: '[05]',
        title: 'Una fila por evento de sesión',
        desc: 'Sesión, tipo de evento, turno y step son columnas reales: filtrar una sesión no implica desarmar JSON.',
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
        ['<code>endpoint</code>', '<em>requerido</em>', 'URL <strong>base</strong> de OTLP. El sufijo <code>/v1/{traces,metrics,logs}</code> lo agrega el plugin.'],
        ['<code>database</code>', '<code>public</code>', 'Se envía como <code>X-Greptime-DB-Name</code>.'],
        ['<code>username</code> / <code>password</code>', '<em>ninguno</em>', 'Basic auth. Los dos o ninguno.'],
        ['<code>signals</code>', 'las tres', 'Cualquier subconjunto de <code>traces</code>, <code>metrics</code>, <code>logs</code>.'],
        ['<code>content</code>', '<code>none</code>', 'Cuánto payload puede salir del proceso.'],
        ['<code>ttl</code>', '<code>180d</code>', 'Retención de las tablas que crea el plugin. También acepta <code>forever</code>.'],
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
      note: 'En ningún modo salen el payload privado <code>meta</code> de una herramienta ni el mensaje y stack de una petición fallida. La proyección es una allowlist, así que un tipo de evento que el plugin no conoce exporta su identidad y nada más.',
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
      desc: 'La lista completa está en el <a href="https://github.com/tma1-ai/dsh-otel#known-limitations">README</a>.',
      cards: [
        {
          title: 'DSH es pre-release',
          desc: 'Renombra y reempaqueta libremente hasta su primer tag, así que el rango de peer queda fijado a la versión contra la que corre CI.',
        },
        {
          title: 'Las convenciones GenAI son experimentales',
          desc: 'Los nombres de atributo vienen de <code>@opentelemetry/semantic-conventions/incubating</code> y se mueven con él.',
        },
        {
          title: '<code>ttl</code> no llega a las tablas de métricas',
          desc: 'En el metric engine la retención es propiedad de la tabla física, así que hay que definirla ahí (<a href="https://github.com/GreptimeTeam/greptimedb/issues/8951">greptimedb#8951</a>).',
        },
        {
          title: 'La exportación es por lotes',
          desc: 'No hay flush por turno, y los registros en vuelo al apagar pueden perderse.',
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
