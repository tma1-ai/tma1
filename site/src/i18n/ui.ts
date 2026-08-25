import type { Locale } from './products';

export interface T {
  lang: Locale;
  title: string;
  description: string;
  nav: { features: string; how: string; security: string };
  hero: {
    hooks: string[]; h1_1: string; h1_2: string; subtitle: string;
    loop_moment: {
      title_bar: string;
      lines: Array<{ kind: 'cmd' | 'comment' | 'tma1' | 'blank'; text?: string }>;
    };
  };
  onboarding: { label: string; manual: string };
  highlights: Array<{ title: string; desc: string }>;
  features: {
    kicker: string; title: string; desc: string;
    cards: Array<{ num: string; title: string; desc: string }>;
  };
  loop_scenarios: {
    intro: string;
    items: Array<{
      kind: string;          // verbatim — do not translate
      severity: 'HIGH' | 'MEDIUM';
      narrative: string;
      suggestion: string;    // verbatim — do not translate
      footer: string;        // verbatim — do not translate
    }>;
  };
  peer_demo: {
    intro: string;
    title_bar: string;
    lines: Array<{ kind: 'prompt' | 'output' | 'blank'; text?: string }>;
  };
  how: {
    kicker: string; title: string; desc: string;
    steps: Array<{ num: string; title: string; desc: string }>;
  };
  security: {
    kicker: string; title: string; desc: string;
    panel_title: string; panel_body: string;
    cards: Array<{ title: string; desc: string }>;
  };
  faq: {
    kicker: string; title: string;
    items: Array<{ q: string; a: string }>;
  };
  footer: { tagline: string };
  ui: { copy: string; copied: string };
}

export const en: T = {
  lang: 'en',
  title: 'TMA1 — local-first observability your agent reads back',
  description: 'TMA1 records every LLM call locally, then routes what it sees into the agent’s next turn. Closed-loop agent self-observation, in one Go binary.',
  nav: { features: 'Features', how: 'How it works', security: 'Security' },
  hero: {
    hooks: [
      'My agent kept editing files I’d just changed by hand. I wanted it to notice.',
      'I needed to know what my agents cost — and whether they were doing anything dangerous.',
      'My agent looped on the same broken test five times. I wanted it to learn from itself.',
    ],
    h1_1: 'A monolith in your agent’s loop.',
    h1_2: 'Silent until it talks back.',
    subtitle: 'TMA1 records every LLM call <em>locally</em>, then routes what it sees back into the agent’s next turn — hooks, MCP tools, and anomaly detection.',
    loop_moment: {
      title_bar: 'claude code · auth.go',
      lines: [
        { kind: 'comment', text: 'edit attempt #4' },
        { kind: 'blank' },
        // verbatim — do not translate (this is what the agent literally reads, from anomaly.go)
        { kind: 'tma1', text: 'Re-read auth.go before the next edit — your in-memory copy is older than what’s on disk.' },
        { kind: 'blank' },
        { kind: 'cmd', text: 'Reading auth.go' },
        { kind: 'comment', text: 'edit succeeded ✓' },
      ],
    },
  },
  onboarding: { label: 'AGENT ONBOARDING', manual: 'Manual install' },
  highlights: [
    { title: 'Your agent learns from its own failures', desc: 'When the same Edit fails three times or a build keeps breaking, TMA1 injects the specific fix path into the next prompt — not into a postmortem next week.' },
    { title: 'Agents read what other agents did', desc: 'Claude Code can pull Codex’s review on the same file, verbatim, via <code>/tma1-peer</code>. No copy-paste between terminal tabs.' },
    { title: 'Nothing leaves your machine', desc: 'One Go binary. No Docker, no cloud. Data stays in <code>~/.tma1/</code>.' },
  ],
  features: {
    kicker: 'Features', title: 'Observability that does something with what it sees',
    desc: 'Closed-loop perception and cross-agent collaboration come first. The dashboards back them up. One Go binary, one local time-series store, no Grafana, no YAML.',
    cards: [
      { num: '01', title: 'Closes the agent loop', desc: 'TMA1 watches for repeated failures, stale views, broken builds. When a rule fires, it writes a concrete fix path into the agent’s next prompt — not into a dashboard for someone to read tomorrow. <strong>Five hooks</strong> deliver it. <strong>Six rules</strong>, each with an actionable suggestion. <strong>HIGH</strong> severity can block <code>Stop</code> so a broken build doesn’t silently ship.' },
      { num: '02', title: 'Cross-agent peer sessions', desc: 'Claude Code reads what Codex left on the same file, <em>verbatim</em>. Codex reads what Claude did. The <code>/tma1-peer</code> skill pulls up to 30 messages plus tool footprint from the peer’s last session on this project. Each agent’s own sessions are excluded by caller-aware filtering — no echo chambers.' },
      { num: '03', title: 'Anomaly detection', desc: 'An agent stuck in a retry loop can burn hundreds of dollars. Each agent view has an Anomalies tab. Click any flagged request to jump straight into the session and see what went wrong.' },
      { num: '04', title: 'Sessions', desc: 'Your agent ran for 25 minutes across 4 turns. What happened? Open the session overlay: left side shows file activity, context breakdown, and API calls. Right side is the full event timeline. Or watch the live canvas while your agent works.' },
      { num: '05', title: 'Tool analytics', desc: 'When your agent feels slow, is it the model or the tool calls? p50 and p95 latency per tool, call counts, success rates, and trend lines.' },
      { num: '06', title: 'Cost breakdown', desc: 'Which model costs the most? Which conversation burned through your budget? Token counts and estimated cost per model, plus burn-rate over time and cache hit ratios.' },
      { num: '07', title: 'Security monitoring', desc: 'Your agent can run shell commands, fetch URLs, and be fed injected prompts. TMA1 flags all of it. For OpenClaw it also tracks webhook errors and stuck sessions.' },
      { num: '08', title: 'Full-text search', desc: 'Type a keyword in the Sessions search tab and it finds matching conversations, tool calls, and results across all sessions. Click a result to open the session at that exact event.' },
    ],
  },
  loop_scenarios: {
    intro: 'When TMA1 sees something the agent should act on, it writes a concrete suggestion into the next prompt. These are real strings from the detector — what the agent literally reads:',
    items: [
      {
        kind: 'repeated_failed_build',
        severity: 'HIGH',
        narrative: 'Wrapped with `tma1 build -- npm test`. Agent retried three times, same error each time.',
        // verbatim — do not translate (anomaly.go::repeated_failed_build, substituted with realistic values)
        suggestion: 'Stop retrying `npm test` and address this error first: TypeError: Cannot read prop ‘user’ of undefined',
        footer: 'injected into next user_prompt_submit',
      },
      {
        kind: 'stale_file_view',
        severity: 'HIGH',
        narrative: 'A human edited the same file the agent was about to overwrite.',
        // verbatim — do not translate (anomaly.go::stale_file_view)
        suggestion: 'Re-read auth.go before the next edit — your in-memory copy is older than what’s on disk.',
        footer: 'injected into next user_prompt_submit',
      },
    ],
  },
  peer_demo: {
    intro: 'Claude Code reads what Codex left, verbatim — via the <code>/tma1-peer</code> skill. It works the other way too.',
    title_bar: 'claude code · in your project',
    lines: [
      { kind: 'prompt', text: '/tma1-peer codex' },
      { kind: 'blank' },
      { kind: 'output', text: 'Codex reviewed auth.go 12 minutes ago and left' },
      { kind: 'output', text: 'three concrete issues:' },
      { kind: 'blank' },
      { kind: 'output', text: '  1. JWT expiration not validated on refresh' },
      { kind: 'output', text: '  2. Session token logged to stderr on auth failure' },
      { kind: 'output', text: '  3. Missing rate-limit on /login' },
      { kind: 'blank' },
      { kind: 'output', text: 'Want me to address all three or pick one?' },
    ],
  },
  how: {
    kicker: 'How it works', title: 'Setup',
    desc: 'Paste the onboarding instruction into your agent and it handles the rest. Or do it yourself:',
    steps: [
      { num: '[1]', title: 'Install', desc: 'One command. Downloads everything into <code>~/.tma1/</code>. No Docker, no system packages.' },
      { num: '[2]', title: 'Configure your agent', desc: 'Point the OTel endpoint to <code>http://localhost:14318/v1/otlp</code>. Works with Claude Code, Codex, OpenClaw, or any OTel SDK. GitHub Copilot CLI needs no config — TMA1 auto-discovers its session logs.' },
      { num: '[3]', title: 'Watch the loop close', desc: 'Browse to <code>localhost:14318</code> for the dashboard. The interesting part happens in your agent: it starts seeing <code>&lt;tma1-context&gt;</code> blocks and acting on them. Optionally wrap dev / test commands with <code>tma1 build -- &lt;command&gt;</code> so build failures feed the loop too (flags: <code>--watch</code>, <code>--tag</code>, <code>--filter-regex</code>). The dashboard is for the human postmortem; the loop is for the agent. For raw SQL, GreptimeDB&rsquo;s own dashboard is at <code>localhost:14000/dashboard</code>.' },
    ],
  },
  security: {
    kicker: 'Security', title: 'Security & Privacy',
    desc: 'Your agent reads your codebase, your API keys, your infrastructure. Sending that to a cloud observability service defeats the purpose. Everything stays local.',
    panel_title: 'How data is stored',
    panel_body: 'TMA1 stores traces and conversation logs on your local disk in <code>~/.tma1/data/</code>. Nothing is uploaded to remote services, and you can inspect or delete the data at any time.',
    cards: [
      { title: 'No network calls', desc: 'After first launch (which downloads the embedded database engine once), TMA1 makes no further network calls. No analytics, no crash reports, no update checks.' },
      { title: 'Fully open source', desc: 'TMA1 is Apache-2.0. Read the code, audit the build, and run it air-gapped.' },
      { title: 'Single binary', desc: '<code>tma1-server</code> runs as one local process and manages its embedded storage engine. No Docker, no system packages, no runtime dependencies.' },
      { title: 'Your data, your disk', desc: 'Delete <code>~/.tma1/</code> and everything is gone. No orphaned cloud state, no remote accounts to close.' },
    ],
  },
  faq: {
    kicker: 'FAQ', title: 'Common questions',
    items: [
      { q: 'Which agents are supported?', a: 'Any agent that emits OpenTelemetry data, plus a few via JSONL auto-discovery. Claude Code sends metrics and logs. Codex sends logs and metrics, and session JSONL is auto-parsed for conversation replay. GitHub Copilot CLI is zero-config: its session JSONL at <code>~/.copilot/session-state/</code> is auto-discovered. OpenClaw sends traces and metrics, and session JSONL is auto-parsed for conversation replay. Any OTel SDK app with GenAI semantic conventions works out of the box. The dashboard auto-detects the data source and shows the right view.' },
      { q: 'Can I query the data with SQL?', a: 'Yes. Run <code>mysql -h 127.0.0.1 -P 14002</code> to connect to the local SQL endpoint, or open <code><a href="http://localhost:14000/dashboard/">localhost:14000/dashboard/</a></code> for the built-in query UI. Traces are in <code>opentelemetry_traces</code>, logs in <code>opentelemetry_logs</code>, session data in <code>tma1_hook_events</code> and <code>tma1_messages</code>, and OTel metrics get auto-created tables.' },
      { q: 'How much disk space does it use?', a: 'It depends on traffic and conversation length. A typical setup uses a few hundred MB per month.' },
    ],
  },
  footer: { tagline: 'Named after TMA-1 from <em>2001: A Space Odyssey</em>. Silently recording everything until you dig it out.' },
  ui: { copy: 'Copy', copied: 'Copied!' },
};

export const zh: T = {
  lang: 'zh',
  title: 'TMA1 — agent 能读回的本地可观测',
  description: 'TMA1 在本地记下 agent 每一次 LLM 调用，再把看到的东西送回 agent 的下一轮 reasoning。一个 Go 二进制里的闭环 agent 自我观测。',
  nav: { features: '功能', how: '工作原理', security: '安全' },
  hero: {
    hooks: [
      'agent 一直在改我刚手动动过的文件。我希望它能注意到。',
      '我想知道 agent 到底花了多少钱、有没有在搞危险操作。',
      'agent 在同一个炸了的测试上 retry 了五次。我希望它能从自己的失败里学。',
    ],
    h1_1: '你的 agent loop 里埋着一块 monolith。',
    h1_2: '安静——直到它开口。',
    subtitle: 'TMA1 在<em>本地</em>记下 agent 每一次 LLM 调用，再通过 hooks、MCP、anomaly 检测把看到的东西送回 agent 的下一轮 reasoning。',
    loop_moment: {
      title_bar: 'claude code · auth.go',
      lines: [
        { kind: 'comment', text: 'edit attempt #4' },
        { kind: 'blank' },
        // verbatim — do not translate
        { kind: 'tma1', text: 'Re-read auth.go before the next edit — your in-memory copy is older than what’s on disk.' },
        { kind: 'blank' },
        { kind: 'cmd', text: 'Reading auth.go' },
        { kind: 'comment', text: 'edit succeeded ✓' },
      ],
    },
  },
  onboarding: { label: 'AGENT 接入', manual: '手动安装' },
  highlights: [
    { title: '你的 agent 会从自己的失败里学', desc: 'Edit 连着失败 3 次、build 一直炸的时候，TMA1 会把具体修复路径塞进 agent 的下一个 prompt——不是下周的 postmortem。' },
    { title: 'agent 能读到别的 agent 留下的东西', desc: 'Claude Code 可以通过 <code>/tma1-peer</code> 把 Codex 在同一个文件上留下的 review 原样拿过来。不用在两个 terminal 之间拷来拷去。' },
    { title: '数据不出本机', desc: '一个 Go 二进制。不要 Docker，不要云。数据只在 <code>~/.tma1/</code>。' },
  ],
  features: {
    kicker: '功能', title: '看到东西后能做点什么的可观测',
    desc: '闭环感知和跨 agent 协作是主轴，dashboard 是补充证据。一个 Go 二进制，本地时序库，不需要 Grafana，不需要 YAML。',
    cards: [
      { num: '01', title: '让 agent 形成闭环', desc: 'TMA1 盯着重复失败、过期视图、坏 build。规则命中的时候，它会把一条具体的修复路径写进 agent 的下一个 prompt——不是塞进 dashboard 让人明天再去看。<strong>五个 hook</strong> 负责送达。<strong>六条规则</strong>，每条都有可执行的建议。<strong>HIGH</strong> 优先级会 block <code>Stop</code>，以免坏的 build 静默上线。' },
      { num: '02', title: '跨 agent 的 peer session', desc: 'Claude Code <em>原样</em>读到 Codex 在同一个文件上留下的内容，反过来也一样。<code>/tma1-peer</code> skill 可以拉到 peer 上次 session 里最多 30 条消息 + 工具足迹。调用方自己的 session 会被过滤掉——避免 echo chamber。' },
      { num: '03', title: '异常检测', desc: 'Agent 卡在重试循环里可以烧掉几百美元。每个 agent 视图有 Anomalies 标签页，点击任何一条异常直接跳到那个 session，看看到底哪儿出了问题。' },
      { num: '04', title: 'Sessions', desc: '你的 agent 跑了 25 分钟。发生了什么？打开 session overlay：左边是文件活动、上下文分布、API 调用明细，右边是完整时间线。或者打开 live canvas，实时看 agent 工作。' },
      { num: '05', title: '工具分析', desc: 'Agent 变慢了，是模型的问题还是工具调用的问题？每个工具的 p50、p95 延迟，调用次数、成功率、趋势线。' },
      { num: '06', title: '费用明细', desc: '哪个模型最贵？哪个对话把预算烧光了？按模型追踪 token 和费用，能看 burn rate 趋势和缓存命中率。' },
      { num: '07', title: '安全监控', desc: '你的 agent 能跑 shell 命令、请求外部 URL、被注入 prompt。TMA1 全部标记。OpenClaw 的 webhook 错误和卡死的 session 也会追踪。' },
      { num: '08', title: '全文搜索', desc: '在 Sessions 搜索框输入关键词，所有 session 的对话和工具调用都能搜到。点击结果直接跳到那个事件。' },
    ],
  },
  loop_scenarios: {
    intro: 'TMA1 判断 agent 应该采取行动的时候，会把一条具体建议写进下一个 prompt。下面这些是检测器里的真实字符串——agent 实际读到的内容：',
    items: [
      {
        kind: 'repeated_failed_build',
        severity: 'HIGH',
        narrative: '用 `tma1 build -- npm test` 包装。Agent 跑了三次，每次都是同一个错误。',
        // verbatim — do not translate
        suggestion: 'Stop retrying `npm test` and address this error first: TypeError: Cannot read prop ‘user’ of undefined',
        footer: 'injected into next user_prompt_submit',
      },
      {
        kind: 'stale_file_view',
        severity: 'HIGH',
        narrative: '人刚手动改了一个文件，agent 准备覆盖这个文件。',
        // verbatim — do not translate
        suggestion: 'Re-read auth.go before the next edit — your in-memory copy is older than what’s on disk.',
        footer: 'injected into next user_prompt_submit',
      },
    ],
  },
  peer_demo: {
    intro: 'Claude Code 通过 <code>/tma1-peer</code> skill 原样读到 Codex 留下的东西。反过来也一样。',
    title_bar: 'claude code · in your project',
    lines: [
      { kind: 'prompt', text: '/tma1-peer codex' },
      { kind: 'blank' },
      { kind: 'output', text: 'Codex reviewed auth.go 12 minutes ago and left' },
      { kind: 'output', text: 'three concrete issues:' },
      { kind: 'blank' },
      { kind: 'output', text: '  1. JWT expiration not validated on refresh' },
      { kind: 'output', text: '  2. Session token logged to stderr on auth failure' },
      { kind: 'output', text: '  3. Missing rate-limit on /login' },
      { kind: 'blank' },
      { kind: 'output', text: 'Want me to address all three or pick one?' },
    ],
  },
  how: {
    kicker: '工作原理', title: '安装配置',
    desc: '把接入指令粘贴给你的 agent，它会自动搞定。或者手动来：',
    steps: [
      { num: '[1]', title: '安装', desc: '一条命令，所有文件装进 <code>~/.tma1/</code>。不需要 Docker，不需要装别的。' },
      { num: '[2]', title: '配置你的 agent', desc: '将 OTel endpoint 指向 <code>http://localhost:14318/v1/otlp</code>。支持 Claude Code、Codex、OpenClaw 或任何 OTel SDK。GitHub Copilot CLI 零配置——TMA1 会自动发现它的 session 日志。' },
      { num: '[3]', title: '看到闭环发生', desc: '浏览器打开 <code>localhost:14318</code> 看 dashboard。有趣的部分发生在 agent 里：它开始看到 <code>&lt;tma1-context&gt;</code> 块并针对性地行动。可选：用 <code>tma1 build -- &lt;command&gt;</code> 包装 dev / test 命令，让 build 失败也进入闭环（支持 <code>--watch</code> / <code>--tag</code> / <code>--filter-regex</code>）。Dashboard 是人事后复盘用的，闭环是给 agent 的。想直接写 SQL 的话，GreptimeDB 自带的 dashboard 在 <code>localhost:14000/dashboard</code>。' },
    ],
  },
  security: {
    kicker: '安全', title: '安全与隐私',
    desc: '你的 agent 能读代码库、API 密钥、基础设施配置。把这些发到云端可观测服务？那还谈什么安全。一切留在本地。',
    panel_title: '数据怎么存的',
    panel_body: 'TMA1 会把 trace 和对话日志保存在本地 <code>~/.tma1/data/</code>。数据不会上传到任何远程服务，你可以随时查看或删除。',
    cards: [
      { title: '零网络请求', desc: '首次启动会自动下载内置数据库引擎，之后 TMA1 不再联系任何外部服务。没有数据上报，没有崩溃报告，没有更新检查。' },
      { title: '完全开源', desc: 'TMA1 采用 Apache-2.0。代码可审计，构建可检查，支持离线运行。' },
      { title: '单一二进制', desc: '<code>tma1-server</code> 以单进程本地运行，并管理内置存储引擎。不要 Docker，不要系统包，没有运行时依赖。' },
      { title: '你的数据，你的磁盘', desc: '删掉 <code>~/.tma1/</code> 就全没了。没有残留的云端状态，没有要注销的远程账号。' },
    ],
  },
  faq: {
    kicker: 'FAQ', title: '常见问题',
    items: [
      { q: '支持哪些 agent？', a: '任何发送 OpenTelemetry 数据的 agent，以及通过 JSONL 自动发现的几个 agent。Claude Code 发送 metrics 和 logs；Codex 发送 logs 和 metrics，会话 JSONL 自动解析用于对话回放。GitHub Copilot CLI 零配置：TMA1 自动发现并解析 <code>~/.copilot/session-state/</code> 下的 session JSONL。OpenClaw 发送 traces 和 metrics，会话 JSONL 也会自动解析。任何遵循 GenAI 语义规范的 OTel SDK 应用开箱即用。Dashboard 根据数据自动切换到对应视图。' },
      { q: '能直接用 SQL 查吗？', a: '能。运行 <code>mysql -h 127.0.0.1 -P 14002</code> 连接本地 SQL 端口，或打开 <code><a href="http://localhost:14000/dashboard/">localhost:14000/dashboard/</a></code> 使用内置查询界面。Traces 在 <code>opentelemetry_traces</code>，logs 在 <code>opentelemetry_logs</code>，session 数据在 <code>tma1_hook_events</code> 和 <code>tma1_messages</code>，OTel metrics 自动建表。' },
      { q: '大概占多少磁盘？', a: '取决于 agent 流量和对话长度。常见场景下，每月大约几百 MB。' },
    ],
  },
  footer: { tagline: '取名自《2001 太空漫游》中的 TMA-1——静默记录一切，等你来挖掘。' },
  ui: { copy: '复制', copied: '已复制！' },
};

export const es: T = {
  lang: 'es',
  title: 'TMA1 — observabilidad local que tu agente lee de vuelta',
  description: 'TMA1 graba cada llamada LLM en tu máquina y reinyecta lo que ve en el próximo turno del agente. Auto-observación en loop cerrado para el agente, en un solo binario Go.',
  nav: { features: 'Funcionalidades', how: 'Cómo funciona', security: 'Seguridad' },
  hero: {
    hooks: [
      'Mi agente seguía editando archivos que yo recién había modificado a mano. Quería que se diera cuenta.',
      'Necesitaba saber cuánto cuestan mis agentes — y si estaban haciendo algo peligroso.',
      'Mi agente volvió a correr el mismo test roto cinco veces. Quería que aprendiera de sus errores.',
    ],
    h1_1: 'Un monolito en el loop de tu agente.',
    h1_2: 'Silencioso, hasta que responde.',
    subtitle: 'TMA1 graba cada llamada LLM <em>localmente</em>, después reinyecta lo que ve en el próximo turno del agente — hooks, MCP y detección de anomalías.',
    loop_moment: {
      title_bar: 'claude code · auth.go',
      lines: [
        { kind: 'comment', text: 'edit attempt #4' },
        { kind: 'blank' },
        // verbatim — do not translate
        { kind: 'tma1', text: 'Re-read auth.go before the next edit — your in-memory copy is older than what’s on disk.' },
        { kind: 'blank' },
        { kind: 'cmd', text: 'Reading auth.go' },
        { kind: 'comment', text: 'edit succeeded ✓' },
      ],
    },
  },
  onboarding: { label: 'ONBOARDING DEL AGENTE', manual: 'Instalación manual' },
  highlights: [
    { title: 'Tu agente aprende de sus propios fallos', desc: 'Cuando el mismo Edit falla tres veces o un build sigue rompiéndose, TMA1 inyecta el camino concreto de solución en el siguiente prompt — no en un postmortem de la semana que viene.' },
    { title: 'Los agentes leen lo que otros agentes hicieron', desc: 'Claude Code puede traer la review de Codex sobre el mismo archivo, palabra por palabra, vía <code>/tma1-peer</code>. Sin copiar y pegar entre pestañas.' },
    { title: 'Nada sale de tu máquina', desc: 'Un solo binario de Go. Sin Docker, sin nube. Los datos se quedan en <code>~/.tma1/</code>.' },
  ],
  features: {
    kicker: 'Funcionalidades', title: 'Observabilidad que hace algo con lo que ve',
    desc: 'Percepción en loop cerrado y colaboración entre agentes primero. Los dashboards quedan como respaldo. Un binario Go, un store de series temporales local, sin Grafana, sin YAML.',
    cards: [
      { num: '01', title: 'Cierra el loop del agente', desc: 'TMA1 vigila fallos repetidos, vistas obsoletas y builds rotos. Cuando una regla se dispara, escribe un camino concreto de solución en el siguiente prompt del agente — no en un dashboard para que alguien lo lea mañana. <strong>Cinco hooks</strong> lo entregan. <strong>Seis reglas</strong>, cada una con una sugerencia accionable. Severidad <strong>HIGH</strong> puede bloquear <code>Stop</code> para que un build roto no se publique en silencio.' },
      { num: '02', title: 'Sesiones de agentes pares', desc: 'Claude Code lee <em>palabra por palabra</em> lo que Codex dejó en el mismo archivo. Codex lee lo que Claude hizo. La skill <code>/tma1-peer</code> trae hasta 30 mensajes más la huella de herramientas de la última sesión del par en este proyecto. Las sesiones propias del agente que llama se excluyen automáticamente — sin cámaras de eco.' },
      { num: '03', title: 'Detección de anomalías', desc: 'Un agente en un loop de reintentos puede quemar cientos de dólares. Cada vista de agente tiene una pestaña Anomalies. Hacé clic en cualquiera para saltar a esa sesión y ver qué salió mal.' },
      { num: '04', title: 'Sessions', desc: 'Tu agente corrió 25 minutos. ¿Qué pasó? Abrí el overlay de sesión: a la izquierda la actividad de archivos, contexto y API calls. A la derecha, el timeline completo. O mirá el canvas en vivo mientras tu agente trabaja.' },
      { num: '05', title: 'Análisis de herramientas', desc: 'Cuando tu agente se siente lento, ¿es el modelo o las herramientas? p50 y p95 de latencia por herramienta, conteos de llamadas, tasas de éxito y líneas de tendencia.' },
      { num: '06', title: 'Desglose de costos', desc: '¿Qué modelo cuesta más? ¿Qué conversación quemó tu presupuesto? Tokens y costo estimado por modelo, más burn rate y ratios de cache hit.' },
      { num: '07', title: 'Monitoreo de seguridad', desc: 'Tu agente puede ejecutar comandos shell, hacer fetches a URLs externas y recibir prompts inyectados. TMA1 marca todo. Para OpenClaw también rastrea errores de webhook y sesiones atascadas.' },
      { num: '08', title: 'Búsqueda de texto completo', desc: 'Escribí una palabra clave en la pestaña de búsqueda de Sessions y aparecen las conversaciones, herramientas y resultados que coinciden. Hacé clic en un resultado para abrir la sesión en ese evento exacto.' },
    ],
  },
  loop_scenarios: {
    intro: 'Cuando TMA1 ve algo sobre lo que el agente debería actuar, escribe una sugerencia concreta en el próximo prompt. Estos son strings reales del detector — lo que el agente literalmente lee:',
    items: [
      {
        kind: 'repeated_failed_build',
        severity: 'HIGH',
        narrative: 'Envuelto con `tma1 build -- npm test`. El agente lo corrió tres veces, el mismo error cada vez.',
        // verbatim — do not translate
        suggestion: 'Stop retrying `npm test` and address this error first: TypeError: Cannot read prop ‘user’ of undefined',
        footer: 'injected into next user_prompt_submit',
      },
      {
        kind: 'stale_file_view',
        severity: 'HIGH',
        narrative: 'Un humano editó el mismo archivo que el agente estaba por sobrescribir.',
        // verbatim — do not translate
        suggestion: 'Re-read auth.go before the next edit — your in-memory copy is older than what’s on disk.',
        footer: 'injected into next user_prompt_submit',
      },
    ],
  },
  peer_demo: {
    intro: 'Claude Code lee lo que Codex dejó, palabra por palabra — vía la skill <code>/tma1-peer</code>. Funciona al revés también.',
    title_bar: 'claude code · in your project',
    lines: [
      { kind: 'prompt', text: '/tma1-peer codex' },
      { kind: 'blank' },
      { kind: 'output', text: 'Codex reviewed auth.go 12 minutes ago and left' },
      { kind: 'output', text: 'three concrete issues:' },
      { kind: 'blank' },
      { kind: 'output', text: '  1. JWT expiration not validated on refresh' },
      { kind: 'output', text: '  2. Session token logged to stderr on auth failure' },
      { kind: 'output', text: '  3. Missing rate-limit on /login' },
      { kind: 'blank' },
      { kind: 'output', text: 'Want me to address all three or pick one?' },
    ],
  },
  how: {
    kicker: 'Cómo funciona', title: 'Configuración',
    desc: 'Pegá la instrucción de onboarding en tu agente y se encarga del resto. O hacelo vos:',
    steps: [
      { num: '[1]', title: 'Instalar', desc: 'Un comando. Todo se descarga en <code>~/.tma1/</code>. Sin Docker, sin paquetes del sistema.' },
      { num: '[2]', title: 'Configurar tu agente', desc: 'Apuntá el endpoint OTel a <code>http://localhost:14318/v1/otlp</code>. Funciona con Claude Code, Codex, OpenClaw o cualquier SDK OTel. GitHub Copilot CLI no necesita configuración — TMA1 detecta sus logs de sesión automáticamente.' },
      { num: '[3]', title: 'Mirá el loop cerrarse', desc: 'Abrí <code>localhost:14318</code> para el dashboard. La parte interesante pasa en tu agente: empieza a ver bloques <code>&lt;tma1-context&gt;</code> y a actuar sobre ellos. Opcional: envolvé tus comandos dev / test con <code>tma1 build -- &lt;command&gt;</code> para que los fallos de build también entren al loop (flags: <code>--watch</code>, <code>--tag</code>, <code>--filter-regex</code>). El dashboard es para el postmortem humano; el loop es para el agente. Para SQL directo, el dashboard propio de GreptimeDB está en <code>localhost:14000/dashboard</code>.' },
    ],
  },
  security: {
    kicker: 'Seguridad', title: 'Seguridad y privacidad',
    desc: 'Tu agente lee tu código, tus API keys, tu infraestructura. Mandar eso a un servicio de observabilidad en la nube anula el propósito. Todo se queda local.',
    panel_title: 'Cómo se almacenan los datos',
    panel_body: 'TMA1 guarda traces y logs de conversación en tu disco local, en <code>~/.tma1/data/</code>. No se sube nada a servicios remotos y podés inspeccionar o borrar los datos cuando quieras.',
    cards: [
      { title: 'Sin llamadas de red', desc: 'Tras el primer inicio (que descarga el motor de base de datos integrado una sola vez), TMA1 no hace más llamadas de red. Sin analíticas, sin reportes de error, sin chequeos de actualización.' },
      { title: 'Completamente open source', desc: 'TMA1 usa licencia Apache-2.0. Leé el código, auditá el build y corrélo sin conexión.' },
      { title: 'Un solo binario', desc: '<code>tma1-server</code> corre como un único proceso local y administra su motor de almacenamiento integrado. Sin Docker, sin paquetes del sistema, sin dependencias runtime.' },
      { title: 'Tus datos, tu disco', desc: 'Borrá <code>~/.tma1/</code> y todo desaparece. Sin estado huérfano en la nube, sin cuentas remotas que cerrar.' },
    ],
  },
  faq: {
    kicker: 'FAQ', title: 'Preguntas frecuentes',
    items: [
      { q: '¿Qué agentes soporta?', a: 'Cualquier agente que emita datos OpenTelemetry, más algunos vía auto-descubrimiento de JSONL. Claude Code envía métricas y logs. Codex envía logs y métricas, y los archivos JSONL de sesión se analizan automáticamente para la reproducción de conversaciones. GitHub Copilot CLI no requiere configuración: sus logs de sesión en <code>~/.copilot/session-state/</code> se detectan automáticamente. OpenClaw envía traces y métricas, y los archivos JSONL de sesión se analizan automáticamente. Cualquier SDK OTel con convenciones semánticas GenAI funciona de entrada. El dashboard detecta automáticamente la fuente de datos y muestra la vista correspondiente.' },
      { q: '¿Se pueden consultar los datos con SQL?', a: 'Sí. Ejecutá <code>mysql -h 127.0.0.1 -P 14002</code> para conectarte al endpoint SQL local, o abrí <code><a href="http://localhost:14000/dashboard/">localhost:14000/dashboard/</a></code> para la interfaz de consultas. Traces en <code>opentelemetry_traces</code>, logs en <code>opentelemetry_logs</code>, datos de sesión en <code>tma1_hook_events</code> y <code>tma1_messages</code>, y las métricas OTel crean tablas automáticamente.' },
      { q: '¿Cuánto disco ocupa?', a: 'Depende de la actividad del agente y del largo de las conversaciones. En un uso típico, unos cientos de MB por mes.' },
    ],
  },
  footer: { tagline: 'Nombrado como TMA-1 de <em>2001: Una odisea del espacio</em>. Registrando todo en silencio hasta que lo descubras.' },
  ui: { copy: 'Copiar', copied: '¡Copiado!' },
};

export const locales = { en, zh, es } as const;
