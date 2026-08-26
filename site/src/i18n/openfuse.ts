import type { ProductCopy } from './product-copy';

const LANGFUSE = 'https://github.com/langfuse/langfuse';
const REPO = 'https://github.com/tma1-ai/openfuse';
const GREPTIMEDB = 'https://github.com/GreptimeTeam/greptimedb';
const DOCKER = 'https://hub.docker.com/r/tma1ai/openfuse-standalone';
const SHOT = '/products/openfuse';
const QUICKSTART = 'OPENFUSE_STANDALONE_IMAGE=tma1ai/openfuse-standalone:1.0.0-beta.1 docker compose -f docker-compose.standalone.yml up -d --pull always';

const shot = (name: string, alt: string, chrome: string) => ({
  kind: 'image' as const,
  src: `${SHOT}/${name}.webp`,
  w: 1440,
  h: 900,
  alt,
  chrome,
});

export const en: ProductCopy = {
  lang: 'en',
  title: 'Openfuse — LLM engineering on a real observability database',
  description:
    'Openfuse is a Langfuse fork that swaps the analytics store from ClickHouse to a time-series observability database. Same product, same SDKs, same public APIs — tracing, evals, prompts, and dashboards, self-hosted under MIT.',
  og: { image: '/products/openfuse/dashboard-home.webp', alt: 'Openfuse home dashboard' },
  nav: [
    { href: '#features', label: 'What works' },
    { href: '#why', label: 'Why' },
    { href: '#status', label: 'Status' },
  ],

  hero: {
    eyebrow: 'Openfuse',
    h1_1: 'LLM engineering on ',
    h1_2: 'a real observability database.',
    subtitle: `A fork of <a href="${LANGFUSE}">Langfuse</a> that swaps the analytics store from ClickHouse to a <a href="${GREPTIMEDB}">time-series observability database</a>. The Langfuse product, public APIs, and SDKs stay the same; <em>the event store becomes the source of truth</em> for traces, observations, scores, and the analytics behind the dashboards.`,
    badges: [
      { label: 'beta', href: `${REPO}/blob/main/docs/known-limitations.md` },
      { label: 'MIT', href: `${REPO}/blob/main/LICENSE` },
      { label: 'Docker Hub', href: DOCKER },
      { label: 'fork of Langfuse v3.184.1', href: LANGFUSE },
    ],
  },

  install: {
    label: '5-MINUTE QUICKSTART',
    cmd: QUICKSTART,
    note: 'Clone the repo, <code>cp .env.quickstart.example .env</code>, then run this. Open <code>localhost:3000</code>: the quickstart env auto-creates a demo project, so you can sign in as <code>demo@example.com</code> / <code>langfuse-dev</code>, or point a Langfuse SDK at the bundled keys immediately. Those are insecure dev defaults — start from <code>.env.prod.example</code> for anything real, and set <code>GREPTIME_PASSWORD</code> to turn on enforced auth on the analytics store.',
    more_summary: 'Pin a published image, or split web and worker',
    more: [
      {
        title: 'standalone — web + worker in one container',
        lines: [
          { kind: 'comment', text: '# pin a tag instead of building from the checkout' },
          { text: 'OPENFUSE_STANDALONE_IMAGE=tma1ai/openfuse-standalone:1.0.0-beta.1 \\' },
          { text: '  docker compose -f docker-compose.standalone.yml up -d --pull always' },
        ],
      },
      {
        title: 'split — scale web and worker independently',
        lines: [
          { text: 'OPENFUSE_WEB_IMAGE=tma1ai/openfuse-web:1.0.0-beta.1 \\' },
          { text: 'OPENFUSE_WORKER_IMAGE=tma1ai/openfuse-worker:1.0.0-beta.1 \\' },
          { text: '  docker compose up -d --pull always' },
        ],
      },
    ],
  },

  highlights: [
    {
      title: 'Your Langfuse SDKs work unchanged',
      desc: 'Point any existing Langfuse SDK — or any OpenTelemetry tracer — at Openfuse. Traces, observations, and scores land with zero code changes.',
    },
    {
      title: 'Start small, scale as you grow',
      desc: 'Begin with a single <code>openfuse-standalone</code>. The store persists to local disk or object storage, and the same engine scales out to a cluster. Scaling back down loses no data.',
    },
    {
      title: 'Cheap long retention',
      desc: 'Object-storage-native tiered storage plus a plain SQL TTL (<code>LANGFUSE_GREPTIME_TTL</code>) make multi-year retention affordable. On ClickHouse-backed Langfuse, configurable retention is an Enterprise feature.',
    },
  ],

  features: {
    id: 'features',
    kicker: 'What works today',
    title: 'The full Langfuse UI, unchanged',
    desc: 'Not a subset. Tracing, dashboards, datasets, evals, and exports all work, and the covered read path is checked byte-for-byte against upstream Langfuse.',
    rows: [
      {
        num: '[01]',
        title: 'Traces and observations',
        desc: 'Explore traces and their nested observation trees with the same search and filtering you already have. Edits, deletions, and exports behave as you would expect, including full project deletion.',
        media: shot('traces-list', 'Openfuse traces list with filters', 'localhost:3000/traces'),
      },
      {
        num: '[02]',
        title: 'One trace, span by span',
        desc: 'Open any trace to walk the full observation tree — inputs, outputs, model, token counts, cost, and latency at every level of nesting.',
        media: shot('trace-detail', 'Openfuse trace detail with a nested observation tree', 'localhost:3000/traces/…'),
      },
      {
        num: '[03]',
        title: 'Dashboards and metrics',
        desc: 'Cost, token usage, latency percentiles, and score analytics, broken down by metadata, tags, and tools. The intentional divergences from upstream — all cases where the fork is equal or more correct — are listed in the parity ledger.',
        media: shot('dashboard-home', 'Openfuse home dashboard — traces, model cost, scores, latency', 'localhost:3000'),
      },
      {
        num: '[04]',
        title: 'Sessions, users, and evals',
        desc: 'Follow a multi-turn conversation end to end, or pivot to everything one user did. Datasets, experiments, and the evaluation workflow all work end to end.',
        media: shot('session-detail', 'Openfuse session view', 'localhost:3000/sessions/…'),
      },
    ],
  },

  sections: [
    {
      kind: 'panel',
      id: 'why',
      kicker: 'Why this store',
      title: 'LLM traces are observability data',
      desc: 'Timestamped wide events with high-cardinality context. That is what a unified observability database is built for.',
      panel_title: 'Where the data lives',
      panel_body: `Metrics, logs, and traces live in <a href="https://docs.greptime.com/user-guide/concepts/why-greptimedb">one engine</a>: SQL and PromQL/TQL queryable, OTLP-native, with compute–storage separation over object storage. Postgres still holds application and config data — users, projects, prompts, dataset definitions, API keys — unchanged from upstream Langfuse. The analytics event store is an append-only <code>raw_events</code> table as the source of truth, plus merged projection tables and indexed EAV side-tables that back metadata, tag, and tool filtering. Redis runs the BullMQ queues.`,
      cards: [
        {
          title: 'No bucket required',
          desc: 'Media uploads, the OTel carrier, the eval blob store, and batch exports all default to local filesystem paths. A stock deployment needs no S3 or MinIO — turn object storage on when you want tiered storage, not to get started.',
        },
        {
          title: 'Directional, not delivered',
          desc: 'Because the events already live in a real observability database, PromQL-native metrics, logs ↔ traces correlation, OTLP-native ingestion, and Flow continuous aggregation become reachable. Those are tracked as ideas in <a href="https://github.com/tma1-ai/openfuse/issues/8">issue #8</a> — not features you can use today.',
        },
      ],
    },
    {
      kind: 'cards',
      id: 'status',
      kicker: 'Project status',
      title: 'Beta',
      desc: 'Try it, run real workloads against it, and open issues. That feedback is what gets it to a stable release.',
      cards: [
        {
          title: 'What is done',
          desc: 'The ClickHouse → GreptimeDB migration is in place, the read path is parity-checked byte-for-byte against upstream, and the full Langfuse product, API, and SDK surface works.',
        },
        {
          title: 'Known limitations',
          desc: '<a href="https://github.com/tma1-ai/openfuse/blob/main/docs/known-limitations.md">Known limitations</a> is a short list of real constraints, plus the few intentional differences from upstream. Skim it before you depend on this.',
        },
        {
          title: 'A community fork',
          desc: 'Openfuse is not affiliated with or endorsed by Langfuse. It retains upstream copyright and attribution.',
        },
        {
          title: 'MIT',
          desc: 'Every feature ships unlocked. No enterprise tier, no commercial license key, no feature gated behind a contract.',
        },
      ],
    },
    {
      kind: 'faq',
      kicker: 'FAQ',
      title: 'Common questions',
      desc: '',
      items: [
        {
          q: 'Do I have to change any code?',
          a: 'No. Openfuse <code>1.0.0-beta.1</code> is based on upstream Langfuse <code>v3.184.1</code>, and existing Langfuse SDKs plus the public ingestion and REST APIs work unchanged. Any OpenTelemetry tracer works too.',
        },
        {
          q: 'How do schema migrations work?',
          a: 'Postgres migrations are upstream Langfuse’s and apply as-is. The GreptimeDB schema is fork-specific and migrates automatically on container startup — idempotent, advisory-lock serialised, and fail-closed.',
        },
        {
          q: 'What exactly differs from upstream Langfuse?',
          a: 'Dashboard and metrics output is checked byte-for-byte against upstream for the covered query surface. The handful of intentional divergences, all cases where the fork is equal or more correct, are in the <a href="https://github.com/tma1-ai/openfuse/blob/main/docs/greptimedb-migration/parity/ledger.md">parity ledger</a>. <a href="https://github.com/tma1-ai/openfuse/blob/main/docs/migration-from-langfuse.md">Migration from Langfuse</a> has the full compatibility statement.',
        },
        {
          q: 'Which images are published?',
          a: 'Three, built for <code>linux/amd64</code> and <code>linux/arm64</code> and pushed on every <code>v*</code> tag: <a href="https://hub.docker.com/r/tma1ai/openfuse-web">openfuse-web</a>, <a href="https://hub.docker.com/r/tma1ai/openfuse-worker">openfuse-worker</a>, and <a href="https://hub.docker.com/r/tma1ai/openfuse-standalone">openfuse-standalone</a> — web and worker in one container, for single-node self-hosting.',
        },
      ],
    },
  ],

  footer: {
    tagline: `A community fork of <a href="${LANGFUSE}">Langfuse</a>, not affiliated with or endorsed by it.`,
    links: [
      { href: REPO, label: 'GitHub' },
      { href: `${REPO}/blob/main/LICENSE`, label: 'MIT' },
      { href: `${REPO}/blob/main/docs/deployment.md`, label: 'Deployment' },
      { href: DOCKER, label: 'Docker Hub' },
    ],
  },
};

export const zh: ProductCopy = {
  ...en,
  lang: 'zh',
  title: 'Openfuse — 跑在真正的可观测数据库上的 LLM 工程平台',
  description:
    'Openfuse 是 Langfuse 的 fork，把分析存储从 ClickHouse 换成了时序可观测数据库。产品、SDK、公共 API 全部不变——tracing、评测、prompt 管理和仪表盘，MIT 协议自托管。',
  nav: [
    { href: '#features', label: '能力' },
    { href: '#why', label: '为什么换' },
    { href: '#status', label: '项目状态' },
  ],

  hero: {
    eyebrow: 'Openfuse',
    h1_1: 'LLM 工程平台，',
    h1_2: '跑在真正的可观测数据库上。',
    subtitle: `<a href="${LANGFUSE}">Langfuse</a> 的一个 fork，把分析存储从 ClickHouse 换成了<a href="${GREPTIMEDB}">时序可观测数据库</a>。Langfuse 的产品、公共 API 和 SDK 都不变；<em>事件存储成为 trace、observation、score 以及仪表盘背后分析的事实来源</em>。`,
    badges: [
      { label: 'beta', href: `${REPO}/blob/main/docs/known-limitations.md` },
      { label: 'MIT', href: `${REPO}/blob/main/LICENSE` },
      { label: 'Docker Hub', href: DOCKER },
      { label: '基于 Langfuse v3.184.1', href: LANGFUSE },
    ],
  },

  install: {
    label: '五分钟上手',
    cmd: QUICKSTART,
    note: '克隆仓库，<code>cp .env.quickstart.example .env</code>，然后执行这一条。打开 <code>localhost:3000</code>：quickstart 配置会自动建好一个 demo 项目，可以用 <code>demo@example.com</code> / <code>langfuse-dev</code> 登录，或者直接把 Langfuse SDK 指向内置的 key。这些都是不安全的开发默认值——正式部署请从 <code>.env.prod.example</code> 出发，并设置 <code>GREPTIME_PASSWORD</code> 打开分析存储的强制鉴权。',
    more_summary: '固定发布镜像，或拆分 web 与 worker',
    more: [
      {
        title: 'standalone —— web + worker 单容器',
        lines: [
          { kind: 'comment', text: '# 用发布镜像代替本地构建' },
          { text: 'OPENFUSE_STANDALONE_IMAGE=tma1ai/openfuse-standalone:1.0.0-beta.1 \\' },
          { text: '  docker compose -f docker-compose.standalone.yml up -d --pull always' },
        ],
      },
      {
        title: 'split —— web 与 worker 独立伸缩',
        lines: [
          { text: 'OPENFUSE_WEB_IMAGE=tma1ai/openfuse-web:1.0.0-beta.1 \\' },
          { text: 'OPENFUSE_WORKER_IMAGE=tma1ai/openfuse-worker:1.0.0-beta.1 \\' },
          { text: '  docker compose up -d --pull always' },
        ],
      },
    ],
  },

  highlights: [
    {
      title: '现有 Langfuse SDK 不用改',
      desc: '把任何现有的 Langfuse SDK——或者任何 OpenTelemetry tracer——指向 Openfuse，trace、observation、score 就会照常落库，不用改一行代码。',
    },
    {
      title: '从小起步，按需扩展',
      desc: '先跑一个 <code>openfuse-standalone</code>。存储可以落本地磁盘，也可以落对象存储，同一个引擎从单机扩展到集群，缩容同样不丢数据。',
    },
    {
      title: '便宜的长期留存',
      desc: '对象存储原生的分层存储，加上一条普通 SQL TTL（<code>LANGFUSE_GREPTIME_TTL</code>），让多年留存变得可负担。在 ClickHouse 版 Langfuse 里，可配置留存是企业版功能。',
    },
  ],

  features: {
    id: 'features',
    kicker: '已经可用',
    title: '完整的 Langfuse 界面，没有裁剪',
    desc: '不是子集。tracing、仪表盘、数据集、评测、导出都可用，覆盖到的读路径逐字节对齐上游 Langfuse。',
    rows: [
      {
        num: '[01]',
        title: 'Trace 与 observation',
        desc: '按你已经熟悉的方式搜索和过滤 trace 及其嵌套的 observation 树。编辑、删除、导出行为与预期一致，包括整个项目的删除。',
        media: shot('traces-list', 'Openfuse trace 列表与过滤器', 'localhost:3000/traces'),
      },
      {
        num: '[02]',
        title: '一条 trace，逐个 span 看',
        desc: '打开任意一条 trace，展开完整的 observation 树——每一层的输入、输出、模型、token 数、成本和延迟。',
        media: shot('trace-detail', 'Openfuse trace 详情与嵌套 observation 树', 'localhost:3000/traces/…'),
      },
      {
        num: '[03]',
        title: '仪表盘与指标',
        desc: '成本、token 用量、延迟分位数、score 分析，可按 metadata、tag、tool 拆分。与上游有意的差异——都是 fork 侧等价或更正确的情况——记录在 parity ledger 里。',
        media: shot('dashboard-home', 'Openfuse 首页仪表盘：trace、模型成本、score、延迟', 'localhost:3000'),
      },
      {
        num: '[04]',
        title: 'Session、用户与评测',
        desc: '完整跟一段多轮对话走到底，或者切换到某个用户做过的全部事情。数据集、实验和评测工作流端到端可用。',
        media: shot('session-detail', 'Openfuse session 视图', 'localhost:3000/sessions/…'),
      },
    ],
  },

  sections: [
    {
      kind: 'panel',
      id: 'why',
      kicker: '为什么换存储',
      title: 'LLM trace 本身就是可观测数据',
      desc: '带时间戳、高基数上下文的宽事件，正是统一可观测数据库要处理的东西。',
      panel_title: '数据分别存在哪里',
      panel_body: `metrics、logs 和 traces 放在<a href="https://docs.greptime.com/user-guide/concepts/why-greptimedb">同一个引擎</a>里：SQL 与 PromQL/TQL 可查，OTLP 原生，存算分离跑在对象存储之上。应用与配置数据——用户、项目、prompt、数据集定义、API key——仍然放在 Postgres，与上游 Langfuse 一致。分析事件存储是一张只追加的 <code>raw_events</code> 表作为事实来源，加上合并后的投影表和带索引的 EAV 侧表，支撑 metadata、tag、tool 过滤。Redis 继续跑 BullMQ 队列。`,
      cards: [
        {
          title: '不需要对象存储桶',
          desc: '媒体上传、OTel carrier、评测 blob 存储、批量导出，默认全部走本地文件系统路径。标准部署完全不需要 S3 或 MinIO——需要分层存储时再打开对象存储，而不是为了跑起来。',
        },
        {
          title: '方向，而非已交付',
          desc: '因为事件本来就存在一个真正的可观测数据库里，PromQL 原生指标、logs ↔ traces 关联、OTLP 原生摄入、Flow 持续聚合都变得可达。这些记在 <a href="https://github.com/tma1-ai/openfuse/issues/8">issue #8</a> 里，是想法，不是今天能用的功能。',
        },
      ],
    },
    {
      kind: 'cards',
      id: 'status',
      kicker: '项目状态',
      title: 'Beta',
      desc: '拿真实负载去跑，遇到问题请提 issue。这些反馈决定它多快走到稳定版。',
      cards: [
        {
          title: '当前进度',
          desc: 'ClickHouse → GreptimeDB 的迁移已经完成，读路径逐字节对齐上游，完整的 Langfuse 产品、API 和 SDK 面都能用。',
        },
        {
          title: '已知限制',
          desc: '<a href="https://github.com/tma1-ai/openfuse/blob/main/docs/known-limitations.md">Known limitations</a> 是一份简短的真实约束清单，外加少数与上游有意的差异。依赖它之前请先过一遍。',
        },
        {
          title: '社区 fork',
          desc: 'Openfuse 与 Langfuse 无隶属关系，也未获其背书，保留上游的版权与署名。',
        },
        {
          title: 'MIT',
          desc: '所有功能都是解锁的。没有企业版，没有商业 license key，也没有需要签合同才能拿到的能力。',
        },
      ],
    },
    {
      kind: 'faq',
      kicker: 'FAQ',
      title: '常见问题',
      desc: '',
      items: [
        {
          q: '需要改代码吗？',
          a: '不需要。Openfuse <code>1.0.0-beta.1</code> 基于上游 Langfuse <code>v3.184.1</code>，现有 Langfuse SDK 以及公共摄入 / REST API 都原样可用。任何 OpenTelemetry tracer 同样可用。',
        },
        {
          q: 'schema 迁移怎么做？',
          a: 'Postgres 迁移就是上游 Langfuse 的，原样执行。GreptimeDB 那套 schema 是 fork 特有的，在容器启动时自动迁移——幂等、用 advisory lock 串行化、失败即中止。',
        },
        {
          q: '和上游 Langfuse 到底差在哪？',
          a: '在覆盖到的查询面上，仪表盘和指标输出逐字节对齐上游。少数有意的差异——都是 fork 侧等价或更正确的情况——记在 <a href="https://github.com/tma1-ai/openfuse/blob/main/docs/greptimedb-migration/parity/ledger.md">parity ledger</a>。完整兼容性声明见 <a href="https://github.com/tma1-ai/openfuse/blob/main/docs/migration-from-langfuse.md">migration-from-langfuse</a>。',
        },
        {
          q: '发布了哪些镜像？',
          a: '三个，构建 <code>linux/amd64</code> 和 <code>linux/arm64</code>，每个 <code>v*</code> tag 推送一次：<a href="https://hub.docker.com/r/tma1ai/openfuse-web">openfuse-web</a>、<a href="https://hub.docker.com/r/tma1ai/openfuse-worker">openfuse-worker</a>，以及 <a href="https://hub.docker.com/r/tma1ai/openfuse-standalone">openfuse-standalone</a>——web 和 worker 装在一个容器里，适合单机自托管。',
        },
      ],
    },
  ],

  footer: {
    tagline: `<a href="${LANGFUSE}">Langfuse</a> 的社区 fork，与其无隶属关系，也未获其背书。`,
    links: [
      { href: REPO, label: 'GitHub' },
      { href: `${REPO}/blob/main/LICENSE`, label: 'MIT' },
      { href: `${REPO}/blob/main/docs/deployment.md`, label: '部署文档' },
      { href: DOCKER, label: 'Docker Hub' },
    ],
  },
};

export const es: ProductCopy = {
  ...en,
  lang: 'es',
  title: 'Openfuse — ingeniería de LLM sobre una base de datos de observabilidad real',
  description:
    'Openfuse es un fork de Langfuse que cambia el almacén analítico de ClickHouse por una base de datos de observabilidad temporal. El mismo producto, los mismos SDKs, las mismas APIs públicas: tracing, evals, prompts y dashboards, autoalojado bajo MIT.',
  nav: [
    { href: '#features', label: 'Qué funciona' },
    { href: '#why', label: 'Por qué' },
    { href: '#status', label: 'Estado' },
  ],

  hero: {
    eyebrow: 'Openfuse',
    h1_1: 'Ingeniería de LLM sobre ',
    h1_2: 'una base de datos de observabilidad real.',
    subtitle: `Un fork de <a href="${LANGFUSE}">Langfuse</a> que cambia el almacén analítico de ClickHouse por una <a href="${GREPTIMEDB}">base de datos de observabilidad temporal</a>. El producto de Langfuse, sus APIs públicas y sus SDKs quedan igual; <em>el almacén de eventos pasa a ser la fuente de verdad</em> de traces, observaciones, scores y del análisis detrás de los dashboards.`,
    badges: [
      { label: 'beta', href: `${REPO}/blob/main/docs/known-limitations.md` },
      { label: 'MIT', href: `${REPO}/blob/main/LICENSE` },
      { label: 'Docker Hub', href: DOCKER },
      { label: 'fork de Langfuse v3.184.1', href: LANGFUSE },
    ],
  },

  install: {
    label: 'ARRANQUE EN 5 MINUTOS',
    cmd: QUICKSTART,
    note: 'Cloná el repo, <code>cp .env.quickstart.example .env</code> y ejecutá esto. Abrí <code>localhost:3000</code>: el entorno de quickstart crea un proyecto demo automáticamente, así que podés entrar con <code>demo@example.com</code> / <code>langfuse-dev</code>, o apuntar un SDK de Langfuse a las claves incluidas. Son valores de desarrollo inseguros — para algo real partí de <code>.env.prod.example</code> y definí <code>GREPTIME_PASSWORD</code> para activar la autenticación del almacén analítico.',
    more_summary: 'Fijar una imagen publicada, o separar web y worker',
    more: [
      {
        title: 'standalone — web + worker en un contenedor',
        lines: [
          { kind: 'comment', text: '# fijá un tag en lugar de construir desde el checkout' },
          { text: 'OPENFUSE_STANDALONE_IMAGE=tma1ai/openfuse-standalone:1.0.0-beta.1 \\' },
          { text: '  docker compose -f docker-compose.standalone.yml up -d --pull always' },
        ],
      },
      {
        title: 'split — escalar web y worker por separado',
        lines: [
          { text: 'OPENFUSE_WEB_IMAGE=tma1ai/openfuse-web:1.0.0-beta.1 \\' },
          { text: 'OPENFUSE_WORKER_IMAGE=tma1ai/openfuse-worker:1.0.0-beta.1 \\' },
          { text: '  docker compose up -d --pull always' },
        ],
      },
    ],
  },

  highlights: [
    {
      title: 'Tus SDKs de Langfuse no cambian',
      desc: 'Apuntá cualquier SDK de Langfuse existente — o cualquier tracer de OpenTelemetry — a Openfuse. Traces, observaciones y scores llegan sin tocar una línea de código.',
    },
    {
      title: 'Empezá chico y escalá',
      desc: 'Arrancá con un solo <code>openfuse-standalone</code>. El almacén persiste en disco local o en almacenamiento de objetos, y el mismo motor escala a un clúster. Volver a bajar de escala no pierde datos.',
    },
    {
      title: 'Retención larga y barata',
      desc: 'El almacenamiento por niveles nativo de objetos más un TTL en SQL plano (<code>LANGFUSE_GREPTIME_TTL</code>) hacen viable retener años de datos. En Langfuse sobre ClickHouse, la retención configurable es una función Enterprise.',
    },
  ],

  features: {
    id: 'features',
    kicker: 'Qué funciona hoy',
    title: 'La UI completa de Langfuse, sin recortes',
    desc: 'No es un subconjunto. Tracing, dashboards, datasets, evals y exportaciones funcionan, y el camino de lectura cubierto se verifica byte a byte contra Langfuse upstream.',
    rows: [
      {
        num: '[01]',
        title: 'Traces y observaciones',
        desc: 'Explorá traces y sus árboles anidados de observaciones con la misma búsqueda y los mismos filtros de siempre. Ediciones, borrados y exportaciones se comportan como esperás, incluida la eliminación completa de un proyecto.',
        media: shot('traces-list', 'Lista de traces de Openfuse con filtros', 'localhost:3000/traces'),
      },
      {
        num: '[02]',
        title: 'Un trace, span por span',
        desc: 'Abrí cualquier trace y recorré el árbol completo de observaciones: entradas, salidas, modelo, tokens, costo y latencia en cada nivel de anidamiento.',
        media: shot('trace-detail', 'Detalle de trace de Openfuse con árbol de observaciones', 'localhost:3000/traces/…'),
      },
      {
        num: '[03]',
        title: 'Dashboards y métricas',
        desc: 'Costo, uso de tokens, percentiles de latencia y análisis de scores, desglosados por metadata, tags y herramientas. Las divergencias intencionales con upstream — todas casos donde el fork es igual o más correcto — están en el parity ledger.',
        media: shot('dashboard-home', 'Dashboard principal de Openfuse: traces, costo por modelo, scores, latencia', 'localhost:3000'),
      },
      {
        num: '[04]',
        title: 'Sesiones, usuarios y evals',
        desc: 'Seguí una conversación de varios turnos de punta a punta, o pasá a todo lo que hizo un usuario. Datasets, experimentos y el flujo de evaluación funcionan de principio a fin.',
        media: shot('session-detail', 'Vista de sesión de Openfuse', 'localhost:3000/sessions/…'),
      },
    ],
  },

  sections: [
    {
      kind: 'panel',
      id: 'why',
      kicker: 'Por qué este almacén',
      title: 'Los traces de LLM son datos de observabilidad',
      desc: 'Eventos anchos con marca de tiempo y contexto de alta cardinalidad. Es justo para lo que está hecha una base de datos de observabilidad unificada.',
      panel_title: 'Dónde vive cada dato',
      panel_body: `Métricas, logs y traces viven en <a href="https://docs.greptime.com/user-guide/concepts/why-greptimedb">un solo motor</a>: consultable con SQL y PromQL/TQL, nativo en OTLP y con separación de cómputo y almacenamiento sobre almacenamiento de objetos. Postgres sigue guardando los datos de aplicación y configuración — usuarios, proyectos, prompts, definiciones de datasets, API keys — igual que en Langfuse upstream. El almacén de eventos analíticos es una tabla <code>raw_events</code> de solo anexado como fuente de verdad, más tablas de proyección fusionadas y tablas EAV indexadas que soportan el filtrado por metadata, tags y herramientas. Redis sigue corriendo las colas de BullMQ.`,
      cards: [
        {
          title: 'Sin bucket obligatorio',
          desc: 'Subidas de medios, el carrier de OTel, el blob store de evals y las exportaciones por lote usan rutas del sistema de archivos local por defecto. Un despliegue estándar no necesita S3 ni MinIO: activá el almacenamiento de objetos cuando quieras niveles, no para arrancar.',
        },
        {
          title: 'Dirección, no entrega',
          desc: 'Como los eventos ya viven en una base de datos de observabilidad real, quedan al alcance las métricas nativas en PromQL, la correlación logs ↔ traces, la ingesta nativa OTLP y la agregación continua con Flow. Están anotadas como ideas en el <a href="https://github.com/tma1-ai/openfuse/issues/8">issue #8</a>, no son funciones disponibles hoy.',
        },
      ],
    },
    {
      kind: 'cards',
      id: 'status',
      kicker: 'Estado del proyecto',
      title: 'Beta',
      desc: 'Probalo, corré cargas reales contra él y abrí issues. Ese feedback es lo que lo lleva a una versión estable.',
      cards: [
        {
          title: 'Qué está hecho',
          desc: 'La migración ClickHouse → GreptimeDB está hecha, el camino de lectura se verifica byte a byte contra upstream, y toda la superficie de producto, API y SDK de Langfuse funciona.',
        },
        {
          title: 'Limitaciones conocidas',
          desc: '<a href="https://github.com/tma1-ai/openfuse/blob/main/docs/known-limitations.md">Known limitations</a> es una lista corta de restricciones reales, más las pocas diferencias intencionales con upstream. Revisala antes de depender de esto.',
        },
        {
          title: 'Un fork comunitario',
          desc: 'Openfuse no está afiliado a Langfuse ni cuenta con su respaldo. Conserva el copyright y la atribución de upstream.',
        },
        {
          title: 'MIT',
          desc: 'Cada función viene desbloqueada. Sin edición enterprise, sin clave de licencia comercial, sin capacidades detrás de un contrato.',
        },
      ],
    },
    {
      kind: 'faq',
      kicker: 'FAQ',
      title: 'Preguntas frecuentes',
      desc: '',
      items: [
        {
          q: '¿Tengo que cambiar código?',
          a: 'No. Openfuse <code>1.0.0-beta.1</code> se basa en Langfuse upstream <code>v3.184.1</code>, y los SDKs existentes junto con las APIs públicas de ingesta y REST funcionan sin cambios. Cualquier tracer de OpenTelemetry también sirve.',
        },
        {
          q: '¿Cómo funcionan las migraciones de esquema?',
          a: 'Las migraciones de Postgres son las de Langfuse upstream y se aplican tal cual. El esquema de GreptimeDB es propio del fork y migra automáticamente al iniciar el contenedor: idempotente, serializado con advisory lock y fail-closed.',
        },
        {
          q: '¿Qué difiere exactamente de Langfuse upstream?',
          a: 'La salida de dashboards y métricas se verifica byte a byte contra upstream en la superficie de consultas cubierta. Las pocas divergencias intencionales, todas casos donde el fork es igual o más correcto, están en el <a href="https://github.com/tma1-ai/openfuse/blob/main/docs/greptimedb-migration/parity/ledger.md">parity ledger</a>. La declaración de compatibilidad completa está en <a href="https://github.com/tma1-ai/openfuse/blob/main/docs/migration-from-langfuse.md">migration-from-langfuse</a>.',
        },
        {
          q: '¿Qué imágenes se publican?',
          a: 'Tres, construidas para <code>linux/amd64</code> y <code>linux/arm64</code> y publicadas en cada tag <code>v*</code>: <a href="https://hub.docker.com/r/tma1ai/openfuse-web">openfuse-web</a>, <a href="https://hub.docker.com/r/tma1ai/openfuse-worker">openfuse-worker</a> y <a href="https://hub.docker.com/r/tma1ai/openfuse-standalone">openfuse-standalone</a> — web y worker en un contenedor, para autoalojamiento en un solo nodo.',
        },
      ],
    },
  ],

  footer: {
    tagline: `Un fork comunitario de <a href="${LANGFUSE}">Langfuse</a>, sin afiliación ni respaldo de su parte.`,
    links: [
      { href: REPO, label: 'GitHub' },
      { href: `${REPO}/blob/main/LICENSE`, label: 'MIT' },
      { href: `${REPO}/blob/main/docs/deployment.md`, label: 'Despliegue' },
      { href: DOCKER, label: 'Docker Hub' },
    ],
  },
};
