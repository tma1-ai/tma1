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
    subtitle: 'TMA1 records every LLM call <em>locally</em>, then routes what it sees back into the agent’s next turn — hooks, MCP tools, anomaly detection, and a searchable history of every session on the machine.',
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
    { title: 'Every past session is searchable', desc: '<code>/tma1-search retry backoff</code> finds the session where you solved it before — yours or another agent’s — and reads back the conversation.' },
    { title: 'Nothing leaves your machine', desc: 'One Go binary. No Docker, no cloud. Data stays in <code>~/.tma1/</code>.' },
  ],
  features: {
    kicker: 'Features', title: 'Observability that does something with what it sees',
    desc: 'Closed-loop perception and cross-agent collaboration come first. The dashboards back them up. One Go binary, one local time-series store, no Grafana, no YAML.',
    cards: [
      { num: '01', title: 'Closes the agent loop', desc: 'TMA1 watches for repeated failures, stale views, broken builds. When a rule fires, it writes a concrete fix path into the agent’s next prompt — not into a dashboard for someone to read tomorrow. <strong>Five hooks</strong> deliver it. <strong>Six rules</strong>, each with an actionable suggestion. <strong>HIGH</strong> severity can block <code>Stop</code> so a broken build doesn’t silently ship.' },
      { num: '02', title: 'Cross-agent peer sessions', desc: 'Claude Code reads what Codex left on the same file, <em>verbatim</em>. Codex reads what Claude did. The <code>/tma1-peer</code> skill pulls the peer’s last session on this project — messages, tools used, files touched. By default an agent doesn’t see its own sessions in the peer list, so no echo chambers; ask for <code>self</code> and it reads its own history back.' },
      { num: '03', title: 'Anomaly detection', desc: 'An agent stuck in a retry loop can burn hundreds of dollars. Each agent view has an Anomalies tab. Click any flagged request to jump straight into the session and see what went wrong.' },
      { num: '04', title: 'Sessions', desc: 'Your agent ran for 25 minutes across 4 turns. What happened? Open the session overlay: left side shows file activity, context breakdown, and API calls. Right side is the full event timeline. Or watch the live canvas while your agent works.' },
      { num: '05', title: 'Tool analytics', desc: 'When your agent feels slow, is it the model or the tool calls? p50 and p95 latency per tool, call counts, success rates, and trend lines.' },
      { num: '06', title: 'Cost breakdown', desc: 'Which model costs the most? Which conversation burned through your budget? Token counts and estimated cost per model, plus burn-rate over time and cache hit ratios.' },
      { num: '07', title: 'Security monitoring', desc: 'Your agent can run shell commands, fetch URLs, and be fed injected prompts. TMA1 flags all of it. For OpenClaw it also tracks webhook errors and stuck sessions.' },
      { num: '08', title: 'Session search — for you and for the agent', desc: 'Type a keyword in the Sessions tab to find matching conversations, tool calls, and results across every session, then click through to that exact event. The agent searches the same data through <code>search_sessions</code>, then reads any session back with <code>get_session_transcript</code>. "How did we fix this last month" becomes a tool call instead of an archaeology project.' },
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
        narrative: 'The file changed outside observed agent writes before the agent edited it.',
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
      { q: 'Can I query the data with SQL?', a: 'Yes. Run <code>mysql -h 127.0.0.1 -P 14002</code> to connect to the local SQL endpoint, or open <code><a href="http://localhost:14000/dashboard/">localhost:14000/dashboard/</a></code> for the built-in query UI. Your agent can query it too, through the <code>exec_query</code> MCP tool — one read-only SELECT per call. Traces are in <code>opentelemetry_traces</code>, logs in <code>opentelemetry_logs</code>, session data in <code>tma1_hook_events</code> and <code>tma1_messages</code>, and OTel metrics get auto-created tables.' },
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
      'agent 一直在改我刚手工改过的文件，我希望它能察觉。',
      '我想知道 agent 到底花了多少钱，有没有执行危险操作。',
      'agent 在同一个失败的测试上重试了五次，我希望它能从自己的失败里学。',
    ],
    h1_1: '你的 agent loop 里埋着一块 monolith。',
    h1_2: '静默，直到它开口。',
    subtitle: 'TMA1 在<em>本地</em>记下 agent 每一次 LLM 调用，再通过 hooks、MCP 和异常检测把观测到的结果送回 agent 的下一轮 reasoning，本机所有会话都可检索回读。',
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
    { title: '你的 agent 会从自己的失败里学', desc: '同一个 Edit 连续失败三次、build 反复失败时，TMA1 会把具体的修复路径写进 agent 的下一个 prompt，而不是留到下周的 postmortem。' },
    { title: 'agent 能读到其他 agent 留下的内容', desc: 'Claude Code 可以通过 <code>/tma1-peer</code> 原样读取 Codex 在同一个文件上留下的 review，不需要在两个终端之间来回复制。' },
    { title: '历史会话可检索', desc: '<code>/tma1-search retry backoff</code> 找出上次解决同一问题的那次会话——你自己的或别的 agent 的——并把对话读回来。' },
    { title: '数据不出本机', desc: '一个 Go 二进制，无需 Docker，不依赖云服务，数据只存放在 <code>~/.tma1/</code>。' },
  ],
  features: {
    kicker: '功能', title: '会对观测结果采取行动的可观测',
    desc: '闭环感知和跨 agent 协作是主轴，dashboard 是补充证据。一个 Go 二进制，本地时序库，不需要 Grafana，不需要 YAML。',
    cards: [
      { num: '01', title: '让 agent 形成闭环', desc: 'TMA1 持续关注重复失败、过期的文件视图和失败的 build。规则命中时，它把一条具体的修复路径写进 agent 的下一个 prompt，而不是写进一块等人明天来看的 dashboard。<strong>五个 hook</strong> 负责送达，<strong>六条规则</strong>各自给出可执行的建议。<strong>HIGH</strong> 级别可以 block <code>Stop</code>，避免失败的 build 静默交付。' },
      { num: '02', title: '跨 agent 的 peer session', desc: 'Claude Code <em>原样</em>读到 Codex 在同一个文件上留下的内容，反过来同样成立。<code>/tma1-peer</code> skill 取回 peer 在这个项目上最近一次 session：消息、用过的工具、动过的文件。默认不返回调用方自己的 session，避免 echo chamber；显式指定 <code>self</code> 就能读回自己的历史。' },
      { num: '03', title: '异常检测', desc: 'agent 卡在重试循环里可以烧掉几百美元。每个 agent 视图都有 Anomalies 标签页，点击任意一条异常可以直接跳到对应 session 定位问题。' },
      { num: '04', title: 'Sessions', desc: 'agent 跑了 25 分钟，中间发生了什么？打开 session overlay：左边是文件活动、上下文分布和 API 调用明细，右边是完整时间线。也可以打开 live canvas 实时观察 agent 工作。' },
      { num: '05', title: '工具分析', desc: 'agent 变慢了，是模型的问题还是工具调用的问题？每个工具的 p50、p95 延迟，调用次数、成功率和趋势线。' },
      { num: '06', title: '费用明细', desc: '哪个模型最贵？哪次对话消耗了大部分预算？按模型追踪 token 和费用，并提供 burn rate 趋势和缓存命中率。' },
      { num: '07', title: '安全监控', desc: 'agent 可以执行 shell 命令、请求外部 URL，也可能被注入 prompt，TMA1 会全部标记。OpenClaw 的 webhook 错误和卡死的 session 同样在追踪范围内。' },
      { num: '08', title: '会话检索——人和 agent 用同一份数据', desc: '在 Sessions 搜索框输入关键词，检索全部 session 的对话和工具调用，点击结果直接跳到对应事件。agent 通过 <code>search_sessions</code> 搜同一份数据，再用 <code>get_session_transcript</code> 把某次会话完整读回来。「上个月这个问题是怎么解决的」从考古变成一次工具调用。' },
    ],
  },
  loop_scenarios: {
    intro: 'TMA1 判断 agent 应该采取行动时，会把一条具体建议写进下一个 prompt。以下是检测器里的真实字符串，也就是 agent 实际读到的内容：',
    items: [
      {
        kind: 'repeated_failed_build',
        severity: 'HIGH',
        narrative: '用 `tma1 build -- npm test` 包装。agent 跑了三次，每次都是同一个错误。',
        // verbatim — do not translate
        suggestion: 'Stop retrying `npm test` and address this error first: TypeError: Cannot read prop ‘user’ of undefined',
        footer: 'injected into next user_prompt_submit',
      },
      {
        kind: 'stale_file_view',
        severity: 'HIGH',
        narrative: '人工刚修改过某个文件，agent 正准备覆盖它。',
        // verbatim — do not translate
        suggestion: 'Re-read auth.go before the next edit — your in-memory copy is older than what’s on disk.',
        footer: 'injected into next user_prompt_submit',
      },
    ],
  },
  peer_demo: {
    intro: 'Claude Code 通过 <code>/tma1-peer</code> skill 原样读到 Codex 留下的内容，反过来同样成立。',
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
    desc: '把接入指令粘贴给 agent，它会自动完成配置。也可以手动操作：',
    steps: [
      { num: '[1]', title: '安装', desc: '一条命令，所有文件安装到 <code>~/.tma1/</code>。不需要 Docker，也不需要额外的系统包。' },
      { num: '[2]', title: '配置你的 agent', desc: '将 OTel endpoint 指向 <code>http://localhost:14318/v1/otlp</code>。支持 Claude Code、Codex、OpenClaw 或任何 OTel SDK。GitHub Copilot CLI 零配置，TMA1 会自动发现它的 session 日志。' },
      { num: '[3]', title: '看到闭环发生', desc: '浏览器打开 <code>localhost:14318</code> 查看 dashboard。真正起作用的部分发生在 agent 里：它开始读到 <code>&lt;tma1-context&gt;</code> 块并据此行动。可选：用 <code>tma1 build -- &lt;command&gt;</code> 包装 dev / test 命令，让 build 失败也进入闭环（支持 <code>--watch</code> / <code>--tag</code> / <code>--filter-regex</code>）。Dashboard 用于人工事后复盘，闭环面向 agent。需要直接写 SQL 时，GreptimeDB 自带的 dashboard 在 <code>localhost:14000/dashboard</code>。' },
    ],
  },
  security: {
    kicker: '安全', title: '安全与隐私',
    desc: '你的 agent 能读到代码库、API 密钥和基础设施配置。把这些发到云端可观测服务，等于放弃了这层安全边界。所有数据留在本地。',
    panel_title: '数据如何存储',
    panel_body: 'TMA1 会把 trace 和对话日志保存在本地 <code>~/.tma1/data/</code>。数据不会上传到任何远程服务，你可以随时查看或删除。',
    cards: [
      { title: '零网络请求', desc: '首次启动会下载一次内置数据库引擎，之后 TMA1 不再发起任何网络请求。没有数据上报，没有崩溃报告，没有更新检查。' },
      { title: '完全开源', desc: 'TMA1 采用 Apache-2.0。代码可审计，构建可检查，支持离线运行。' },
      { title: '单一二进制', desc: '<code>tma1-server</code> 以单进程本地运行，并管理内置存储引擎。不需要 Docker、系统包和运行时依赖。' },
      { title: '你的数据，你的磁盘', desc: '删除 <code>~/.tma1/</code>，数据即全部消失。没有残留的云端状态，也没有需要注销的远程账号。' },
    ],
  },
  faq: {
    kicker: 'FAQ', title: '常见问题',
    items: [
      { q: '支持哪些 agent？', a: '任何发送 OpenTelemetry 数据的 agent，以及通过 JSONL 自动发现的几个 agent。Claude Code 发送 metrics 和 logs；Codex 发送 logs 和 metrics，会话 JSONL 自动解析用于对话回放。GitHub Copilot CLI 零配置：TMA1 自动发现并解析 <code>~/.copilot/session-state/</code> 下的 session JSONL。OpenClaw 发送 traces 和 metrics，会话 JSONL 也会自动解析。任何遵循 GenAI 语义规范的 OTel SDK 应用开箱即用。Dashboard 根据数据自动切换到对应视图。' },
      { q: '能直接用 SQL 查吗？', a: '能。运行 <code>mysql -h 127.0.0.1 -P 14002</code> 连接本地 SQL 端口，或打开 <code><a href="http://localhost:14000/dashboard/">localhost:14000/dashboard/</a></code> 使用内置查询界面。agent 也可以查——通过 <code>exec_query</code> MCP 工具，每次一条只读 SELECT。Traces 在 <code>opentelemetry_traces</code>，logs 在 <code>opentelemetry_logs</code>，session 数据在 <code>tma1_hook_events</code> 和 <code>tma1_messages</code>，OTel metrics 自动建表。' },
      { q: '大约占用多少磁盘？', a: '取决于 agent 流量和对话长度。常见场景下，每月大约几百 MB。' },
    ],
  },
  footer: { tagline: '名字取自《2001 太空漫游》中的 TMA-1：静默记录一切，直到被挖掘出来。' },
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
    subtitle: 'TMA1 graba cada llamada LLM <em>localmente</em>, después reinyecta lo que ve en el próximo turno del agente — hooks, MCP, detección de anomalías y un historial de sesiones que se puede buscar.',
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
    { title: 'Cada sesión pasada se puede buscar', desc: '<code>/tma1-search retry backoff</code> encuentra la sesión donde ya lo resolviste — tuya o de otro agente — y recupera la conversación.' },
    { title: 'Nada sale de tu máquina', desc: 'Un solo binario de Go. Sin Docker, sin nube. Los datos se quedan en <code>~/.tma1/</code>.' },
  ],
  features: {
    kicker: 'Funcionalidades', title: 'Observabilidad que hace algo con lo que ve',
    desc: 'Percepción en loop cerrado y colaboración entre agentes primero. Los dashboards quedan como respaldo. Un binario Go, un store de series temporales local, sin Grafana, sin YAML.',
    cards: [
      { num: '01', title: 'Cierra el loop del agente', desc: 'TMA1 vigila fallos repetidos, vistas obsoletas y builds rotos. Cuando una regla se dispara, escribe un camino concreto de solución en el siguiente prompt del agente — no en un dashboard para que alguien lo lea mañana. <strong>Cinco hooks</strong> lo entregan. <strong>Seis reglas</strong>, cada una con una sugerencia accionable. Severidad <strong>HIGH</strong> puede bloquear <code>Stop</code> para que un build roto no se publique en silencio.' },
      { num: '02', title: 'Sesiones de agentes pares', desc: 'Claude Code lee <em>palabra por palabra</em> lo que Codex dejó en el mismo archivo. Codex lee lo que Claude hizo. La skill <code>/tma1-peer</code> trae la última sesión del par en este proyecto: mensajes, herramientas usadas, archivos tocados. Por defecto un agente no ve sus propias sesiones en la lista de pares — sin cámaras de eco; pide <code>self</code> y recupera su propio historial.' },
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
        narrative: 'El archivo cambió fuera de las escrituras observadas del agente antes de que el agente lo editara.',
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
