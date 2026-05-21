export interface T {
  lang: string;
  title: string;
  description: string;
  nav: { features: string; how: string; security: string };
  hero: { hook: string; h1_1: string; h1_2: string; subtitle: string };
  onboarding: { label: string; manual: string };
  highlights: Array<{ title: string; desc: string }>;
  features: {
    kicker: string; title: string; desc: string;
    cards: Array<{ num: string; title: string; desc: string }>;
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
  quickstart: { kicker: string; title: string; desc: string; manual: string };
  footer: { tagline: string };
  ui: { copy: string; copied: string; theme_light: string; theme_dark: string; theme_system: string };
}

export const en: T = {
  lang: 'en',
  title: 'TMA1 \u2014 Local-First LLM Agent Observability',
  description: 'Local observability for your AI agents. Tokens, cost, latency, and conversation replay \u2014 on your machine.',
  nav: { features: 'Features', how: 'How it works', security: 'Security' },
  hero: {
    hook: 'I needed to know what my agents cost \u2014 and whether they were doing anything dangerous.',
    h1_1: 'Know what your agent is doing',
    h1_2: 'and what it costs',
    subtitle: 'Tokens, cost, latency \u2014 every LLM call, recorded <em>locally</em>. Click a spike to see which conversation is burning money.',
  },
  onboarding: { label: 'AGENT ONBOARDING', manual: 'Manual install' },
  highlights: [
    { title: 'See what happened', desc: 'An agent ran for 20 minutes. What did it do? Open the dashboard, click a trace, read the full conversation.' },
    { title: 'Catch the expensive calls', desc: 'That $40 session last Tuesday \u2014 was it one huge context window or a runaway retry loop? Now you know.' },
    { title: 'Nothing leaves your machine', desc: 'Your agent sees your codebase, your env vars, your keys. That data stays in <code>~/.tma1/</code>, never uploaded anywhere.' },
  ],
  features: {
    kicker: 'Features', title: 'Observability without the overhead',
    desc: 'Seven views for Claude Code, Codex, Copilot CLI, OpenClaw, OTel GenAI, Sessions, and Prompts. The dashboard picks the right one from your data. No Grafana, no cloud, no YAML.',
    cards: [
      { num: '01', title: 'Cost breakdown', desc: 'Which model costs the most? Which conversation burned through your budget? Token counts and estimated cost per model, plus burn-rate over time and cache hit ratios.' },
      { num: '02', title: 'Anomaly detection', desc: 'An agent stuck in a retry loop can burn hundreds of dollars. Each agent view has an Anomalies tab. Click any flagged request to jump straight into that session and see what went wrong.' },
      { num: '03', title: 'Sessions', desc: 'Your agent ran for 25 minutes across 4 turns. What happened? Open the session overlay: left side shows file activity, context breakdown, and API calls. Right side is the full event timeline. Or watch the live canvas while your agent works.' },
      { num: '04', title: 'Tool analytics', desc: 'When your agent feels slow, is it the model or the tool calls? p50 and p95 latency per tool, call counts, success rates, and trend lines.' },
      { num: '05', title: 'Security monitoring', desc: 'Your agent can run shell commands, fetch URLs, and be fed injected prompts. TMA1 flags all of it. For OpenClaw it also tracks webhook errors and stuck sessions.' },
      { num: '06', title: 'Full-text search', desc: 'Type a keyword in the Sessions search tab and it finds matching conversations, tool calls, and results across all sessions. Click a result to open the session at that exact event.' },
      { num: '07', title: 'Closes the agent loop', desc: 'Observability is half the story. TMA1 also feeds what it sees back to the agent. Five Claude Code hooks inject a session digest, prior state, and live anomalies into the next turn. Seven MCP tools let the agent pull session state, build status, external changes, or peer-agent sessions on demand. Six anomaly rules route through the right channel — stale-view edits and repeated build failures block <code>Stop</code>; lower-priority signals land in the next user prompt. <code>/tma1-peer codex</code> reads what Codex just left on this project and brings it into Claude’s context, so no copy-paste between terminals.' },
    ],
  },
  how: {
    kicker: 'How it works', title: 'Setup',
    desc: 'Paste the onboarding instruction into your agent and it handles the rest. Or do it yourself:',
    steps: [
      { num: '[1]', title: 'Install', desc: 'One command. Downloads everything into <code>~/.tma1/</code>. No Docker, no system packages.' },
      { num: '[2]', title: 'Configure your agent', desc: 'Point the OTel endpoint to <code>http://localhost:14318/v1/otlp</code>. Works with Claude Code, Codex, OpenClaw, or any OTel SDK. GitHub Copilot CLI needs no config \u2014 TMA1 auto-discovers its session logs.' },
      { num: '[3]', title: 'Open the dashboard', desc: 'Browse to <code>localhost:14318</code>. Traces show up seconds after your agent\u2019s next LLM call.' },
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
  quickstart: {
    kicker: 'Quick start', title: 'Try it now',
    desc: 'Paste this into your agent. It reads the skill file and handles the rest.',
    manual: 'Or install manually',
  },
  footer: { tagline: 'Named after TMA-1 from <em>2001: A Space Odyssey</em>. Silently recording everything until you dig it out.' },
  ui: { copy: 'Copy', copied: 'Copied!', theme_light: 'Light', theme_dark: 'Dark', theme_system: 'System' },
};

export const zh: T = {
  lang: 'zh',
  title: 'TMA1 \u2014 LLM Agent \u672c\u5730\u53ef\u89c2\u6d4b',
  description: 'AI agent \u672c\u5730\u53ef\u89c2\u6d4b\u3002Token\u3001\u8d39\u7528\u3001\u5ef6\u8fdf\u3001\u5bf9\u8bdd\u56de\u653e\u2014\u2014\u5168\u90e8\u5728\u4f60\u7684\u673a\u5668\u4e0a\u3002',
  nav: { features: '\u529f\u80fd', how: '\u5de5\u4f5c\u539f\u7406', security: '\u5b89\u5168' },
  hero: {
    hook: '\u6211\u60f3\u77e5\u9053 agent \u5230\u5e95\u82b1\u4e86\u591a\u5c11\u94b1\uff0c\u6709\u6ca1\u6709\u5728\u641e\u5371\u9669\u64cd\u4f5c\u3002',
    h1_1: '\u4f60\u7684 agent \u5728\u505a\u4ec0\u4e48',
    h1_2: '\u82b1\u4e86\u591a\u5c11\u94b1',
    subtitle: 'Token\u3001\u8d39\u7528\u3001\u5ef6\u8fdf\u2014\u2014\u6bcf\u6b21 LLM \u8c03\u7528\uff0c<em>\u672c\u5730</em>\u8bb0\u5f55\u3002\u70b9\u5f00\u4e00\u4e2a\u5c16\u5cf0\uff0c\u770b\u54ea\u4e2a\u5bf9\u8bdd\u5728\u70e7\u94b1\u3002',
  },
  onboarding: { label: 'AGENT \u63a5\u5165', manual: '\u624b\u52a8\u5b89\u88c5' },
  highlights: [
    { title: '\u770b\u770b\u53d1\u751f\u4e86\u4ec0\u4e48', desc: '\u4e00\u4e2a agent \u8dd1\u4e86 20 \u5206\u949f\u3002\u5b83\u5e72\u4e86\u4ec0\u4e48\uff1f\u6253\u5f00 dashboard\uff0c\u70b9\u5f00\u4e00\u6761 trace\uff0c\u770b\u5b8c\u6574\u5bf9\u8bdd\u3002' },
    { title: '\u6293\u4f4f\u70e7\u94b1\u7684\u8c03\u7528', desc: '\u4e0a\u5468\u4e8c\u90a3\u4e2a $40 \u7684 session\u2014\u2014\u662f context window \u592a\u5927\u8fd8\u662f\u91cd\u8bd5\u6b7b\u5faa\u73af\uff1f\u73b0\u5728\u4e00\u773c\u770b\u51fa\u6765\u3002' },
    { title: '\u6570\u636e\u4e0d\u51fa\u672c\u673a', desc: '\u4f60\u7684 agent \u80fd\u770b\u5230\u4ee3\u7801\u3001\u73af\u5883\u53d8\u91cf\u3001\u5bc6\u94a5\u3002\u8fd9\u4e9b\u6570\u636e\u53ea\u7559\u5728 <code>~/.tma1/</code>\uff0c\u4e0d\u4f1a\u4e0a\u4f20\u4efb\u4f55\u5730\u65b9\u3002' },
  ],
  features: {
    kicker: '\u529f\u80fd', title: '\u8f7b\u91cf\u53ef\u89c2\u6d4b',
    desc: 'Claude Code、Codex、Copilot CLI、OpenClaw、OTel GenAI、Sessions、Prompts 七个视图，根据数据自动切换。不用装 Grafana，不用云服务，不用写 YAML。',
    cards: [
      { num: '01', title: '\u8d39\u7528\u660e\u7ec6', desc: '\u54ea\u4e2a\u6a21\u578b\u6700\u8d35\uff1f\u54ea\u4e2a\u5bf9\u8bdd\u628a\u9884\u7b97\u70e7\u5149\u4e86\uff1f\u6309\u6a21\u578b\u8ffd\u8e2a token \u548c\u8d39\u7528\uff0c\u80fd\u770b burn rate \u8d8b\u52bf\u548c\u7f13\u5b58\u547d\u4e2d\u7387\u3002' },
      { num: '02', title: '\u5f02\u5e38\u68c0\u6d4b', desc: 'Agent \u5361\u5728\u91cd\u8bd5\u5faa\u73af\u91cc\u53ef\u4ee5\u70e7\u6389\u51e0\u767e\u7f8e\u5143\u3002\u6bcf\u4e2a agent \u89c6\u56fe\u6709 Anomalies \u6807\u7b7e\u9875\uff0c\u70b9\u51fb\u4efb\u4f55\u4e00\u6761\u5f02\u5e38\u76f4\u63a5\u8df3\u5230\u90a3\u4e2a session\uff0c\u770b\u770b\u5230\u5e95\u54ea\u513f\u51fa\u4e86\u95ee\u9898\u3002' },
      { num: '03', title: 'Sessions', desc: '\u4f60\u7684 agent \u8dd1\u4e86 25 \u5206\u949f\u3002\u53d1\u751f\u4e86\u4ec0\u4e48\uff1f\u6253\u5f00 session overlay\uff1a\u5de6\u8fb9\u662f\u6587\u4ef6\u6d3b\u52a8\u3001\u4e0a\u4e0b\u6587\u5206\u5e03\u3001API \u8c03\u7528\u660e\u7ec6\uff0c\u53f3\u8fb9\u662f\u5b8c\u6574\u65f6\u95f4\u7ebf\u3002\u6216\u8005\u6253\u5f00 live canvas\uff0c\u5b9e\u65f6\u770b agent \u5de5\u4f5c\u3002' },
      { num: '04', title: '\u5de5\u5177\u5206\u6790', desc: 'Agent \u53d8\u6162\u4e86\uff0c\u662f\u6a21\u578b\u7684\u95ee\u9898\u8fd8\u662f\u5de5\u5177\u8c03\u7528\u7684\u95ee\u9898\uff1f\u6bcf\u4e2a\u5de5\u5177\u7684 p50\u3001p95 \u5ef6\u8fdf\uff0c\u8c03\u7528\u6b21\u6570\u3001\u6210\u529f\u7387\u3001\u8d8b\u52bf\u7ebf\u3002' },
      { num: '05', title: '\u5b89\u5168\u76d1\u63a7', desc: '\u4f60\u7684 agent \u80fd\u8dd1 shell \u547d\u4ee4\u3001\u8bf7\u6c42\u5916\u90e8 URL\u3001\u88ab\u6ce8\u5165 prompt\u3002TMA1 \u5168\u90e8\u6807\u8bb0\u3002OpenClaw \u7684 webhook \u9519\u8bef\u548c\u5361\u6b7b\u7684 session \u4e5f\u4f1a\u8ffd\u8e2a\u3002' },
      { num: '06', title: '\u5168\u6587\u641c\u7d22', desc: '\u5728 Sessions \u641c\u7d22\u6846\u8f93\u5165\u5173\u952e\u8bcd\uff0c\u6240\u6709 session \u7684\u5bf9\u8bdd\u548c\u5de5\u5177\u8c03\u7528\u90fd\u80fd\u641c\u5230\u3002\u70b9\u51fb\u7ed3\u679c\u76f4\u63a5\u8df3\u5230\u90a3\u4e2a\u4e8b\u4ef6\u3002' },
      { num: '07', title: '\u8ba9 agent \u5f62\u6210\u95ed\u73af', desc: '\u89c2\u6d4b\u53ea\u662f\u4e00\u534a\u3002TMA1 \u4f1a\u628a\u770b\u5230\u7684\u4e1c\u897f\u56de\u705c\u7ed9 agent\u3002\u4e94\u4e2a Claude Code hook \u5728\u4e0b\u4e00\u8f6e\u628a session \u6458\u8981\u3001\u4e0a\u4e0b\u6587\u72b6\u6001\u548c\u5b9e\u65f6 anomaly \u6ce8\u5165 prompt\u3002\u4e03\u4e2a MCP \u5de5\u5177\u8ba9 agent \u6309\u9700\u62c9\u53d6 session \u72b6\u6001\u3001build \u72b6\u6001\u3001\u5916\u90e8\u53d8\u66f4\u3001\u540c\u9879\u76ee\u4e0a\u522b\u7684 agent \u4f1a\u8bdd\u3002\u516d\u6761 anomaly \u89c4\u5219\u6309\u4f18\u5148\u7ea7\u8d70\u4e0d\u540c\u6ce8\u5165\u901a\u9053\u2014\u2014stale-view \u7f16\u8f91\u3001\u91cd\u590d\u6784\u5efa\u5931\u8d25\u4f1a block <code>Stop</code>\uff1b\u4f4e\u4f18\u5148\u7ea7\u4fe1\u53f7\u8d70\u4e0b\u6b21 user prompt\u3002<code>/tma1-peer codex</code> \u4e00\u952e\u628a Codex \u521a\u5728\u8fd9\u4e2a\u9879\u76ee\u7559\u4e0b\u7684\u5185\u5bb9\u5e26\u5165 Claude \u7684\u4e0a\u4e0b\u6587\uff0c\u4e0d\u7528\u5728 terminal \u4e4b\u95f4\u62f7\u6765\u62f7\u53bb\u3002' },
    ],
  },
  how: {
    kicker: '\u5de5\u4f5c\u539f\u7406', title: '\u5b89\u88c5\u914d\u7f6e',
    desc: '\u628a\u63a5\u5165\u6307\u4ee4\u7c98\u8d34\u7ed9\u4f60\u7684 agent\uff0c\u5b83\u4f1a\u81ea\u52a8\u641e\u5b9a\u3002\u6216\u8005\u624b\u52a8\u6765\uff1a',
    steps: [
      { num: '[1]', title: '\u5b89\u88c5', desc: '\u4e00\u6761\u547d\u4ee4\uff0c\u6240\u6709\u6587\u4ef6\u88c5\u8fdb <code>~/.tma1/</code>\u3002\u4e0d\u9700\u8981 Docker\uff0c\u4e0d\u9700\u8981\u88c5\u522b\u7684\u3002' },
      { num: '[2]', title: '\u914d\u7f6e\u4f60\u7684 agent', desc: '\u5c06 OTel endpoint \u6307\u5411 <code>http://localhost:14318/v1/otlp</code>\u3002\u652f\u6301 Claude Code\u3001Codex\u3001OpenClaw \u6216\u4efb\u4f55 OTel SDK\u3002GitHub Copilot CLI \u96f6\u914d\u7f6e\u2014\u2014TMA1 \u4f1a\u81ea\u52a8\u53d1\u73b0\u5b83\u7684 session \u65e5\u5fd7\u3002' },
      { num: '[3]', title: '\u6253\u5f00 dashboard', desc: '\u6d4f\u89c8\u5668\u6253\u5f00 <code>localhost:14318</code>\u3002agent \u4e0b\u6b21\u8c03 LLM \u540e\u51e0\u79d2\u5c31\u80fd\u770b\u5230 trace\u3002' },
    ],
  },
  security: {
    kicker: '\u5b89\u5168', title: '\u5b89\u5168\u4e0e\u9690\u79c1',
    desc: '\u4f60\u7684 agent \u80fd\u8bfb\u4ee3\u7801\u5e93\u3001API \u5bc6\u94a5\u3001\u57fa\u7840\u8bbe\u65bd\u914d\u7f6e\u3002\u628a\u8fd9\u4e9b\u53d1\u5230\u4e91\u7aef\u53ef\u89c2\u6d4b\u670d\u52a1\uff1f\u90a3\u8fd8\u8c08\u4ec0\u4e48\u5b89\u5168\u3002\u4e00\u5207\u7559\u5728\u672c\u5730\u3002',
    panel_title: '\u6570\u636e\u600e\u4e48\u5b58\u7684',
    panel_body: 'TMA1 \u4f1a\u628a trace \u548c\u5bf9\u8bdd\u65e5\u5fd7\u4fdd\u5b58\u5728\u672c\u5730 <code>~/.tma1/data/</code>\u3002\u6570\u636e\u4e0d\u4f1a\u4e0a\u4f20\u5230\u4efb\u4f55\u8fdc\u7a0b\u670d\u52a1\uff0c\u4f60\u53ef\u4ee5\u968f\u65f6\u67e5\u770b\u6216\u5220\u9664\u3002',
    cards: [
      { title: '\u96f6\u7f51\u7edc\u8bf7\u6c42', desc: '\u9996\u6b21\u542f\u52a8\u4f1a\u81ea\u52a8\u4e0b\u8f7d\u5185\u7f6e\u6570\u636e\u5e93\u5f15\u64ce\uff0c\u4e4b\u540e TMA1 \u4e0d\u518d\u8054\u7cfb\u4efb\u4f55\u5916\u90e8\u670d\u52a1\u3002\u6ca1\u6709\u6570\u636e\u4e0a\u62a5\uff0c\u6ca1\u6709\u5d29\u6e83\u62a5\u544a\uff0c\u6ca1\u6709\u66f4\u65b0\u68c0\u67e5\u3002' },
      { title: '\u5b8c\u5168\u5f00\u6e90', desc: 'TMA1 \u91c7\u7528 Apache-2.0\u3002\u4ee3\u7801\u53ef\u5ba1\u8ba1\uff0c\u6784\u5efa\u53ef\u68c0\u67e5\uff0c\u652f\u6301\u79bb\u7ebf\u8fd0\u884c\u3002' },
      { title: '\u5355\u4e00\u4e8c\u8fdb\u5236', desc: '<code>tma1-server</code> \u4ee5\u5355\u8fdb\u7a0b\u672c\u5730\u8fd0\u884c\uff0c\u5e76\u7ba1\u7406\u5185\u7f6e\u5b58\u50a8\u5f15\u64ce\u3002\u4e0d\u8981 Docker\uff0c\u4e0d\u8981\u7cfb\u7edf\u5305\uff0c\u6ca1\u6709\u8fd0\u884c\u65f6\u4f9d\u8d56\u3002' },
      { title: '\u4f60\u7684\u6570\u636e\uff0c\u4f60\u7684\u78c1\u76d8', desc: '\u5220\u6389 <code>~/.tma1/</code> \u5c31\u5168\u6ca1\u4e86\u3002\u6ca1\u6709\u6b8b\u7559\u7684\u4e91\u7aef\u72b6\u6001\uff0c\u6ca1\u6709\u8981\u6ce8\u9500\u7684\u8fdc\u7a0b\u8d26\u53f7\u3002' },
    ],
  },
  faq: {
    kicker: 'FAQ', title: '\u5e38\u89c1\u95ee\u9898',
    items: [
      { q: '\u652f\u6301\u54ea\u4e9b agent\uff1f', a: '\u4efb\u4f55\u53d1\u9001 OpenTelemetry \u6570\u636e\u7684 agent\uff0c\u4ee5\u53ca\u901a\u8fc7 JSONL \u81ea\u52a8\u53d1\u73b0\u7684\u51e0\u4e2a agent\u3002Claude Code \u53d1\u9001 metrics \u548c logs\uff1bCodex \u53d1\u9001 logs \u548c metrics\uff0c\u4f1a\u8bdd JSONL \u81ea\u52a8\u89e3\u6790\u7528\u4e8e\u5bf9\u8bdd\u56de\u653e\u3002GitHub Copilot CLI \u96f6\u914d\u7f6e\uff1aTMA1 \u81ea\u52a8\u53d1\u73b0\u5e76\u89e3\u6790 <code>~/.copilot/session-state/</code> \u4e0b\u7684 session JSONL\u3002OpenClaw \u53d1\u9001 traces \u548c metrics\uff0c\u4f1a\u8bdd JSONL \u4e5f\u4f1a\u81ea\u52a8\u89e3\u6790\u3002\u4efb\u4f55\u9075\u5faa GenAI \u8bed\u4e49\u89c4\u8303\u7684 OTel SDK \u5e94\u7528\u5f00\u7bb1\u5373\u7528\u3002Dashboard \u6839\u636e\u6570\u636e\u81ea\u52a8\u5207\u6362\u5230\u5bf9\u5e94\u89c6\u56fe\u3002' },
      { q: '\u80fd\u76f4\u63a5\u7528 SQL \u67e5\u5417\uff1f', a: '\u80fd\u3002\u8fd0\u884c <code>mysql -h 127.0.0.1 -P 14002</code> \u8fde\u63a5\u672c\u5730 SQL \u7aef\u53e3\uff0c\u6216\u6253\u5f00 <code><a href="http://localhost:14000/dashboard/">localhost:14000/dashboard/</a></code> \u4f7f\u7528\u5185\u7f6e\u67e5\u8be2\u754c\u9762\u3002Traces \u5728 <code>opentelemetry_traces</code>\uff0clogs \u5728 <code>opentelemetry_logs</code>\uff0csession \u6570\u636e\u5728 <code>tma1_hook_events</code> \u548c <code>tma1_messages</code>\uff0cOTel metrics \u81ea\u52a8\u5efa\u8868\u3002' },
      { q: '\u5927\u6982\u5360\u591a\u5c11\u78c1\u76d8\uff1f', a: '\u53d6\u51b3\u4e8e agent \u6d41\u91cf\u548c\u5bf9\u8bdd\u957f\u5ea6\u3002\u5e38\u89c1\u573a\u666f\u4e0b\uff0c\u6bcf\u6708\u5927\u7ea6\u51e0\u767e MB\u3002' },
    ],
  },
  quickstart: {
    kicker: '\u5feb\u901f\u5f00\u59cb', title: '\u73b0\u5728\u8bd5\u8bd5',
    desc: '\u628a\u8fd9\u6bb5\u8bdd\u7c98\u8d34\u7ed9\u4f60\u7684 agent\uff0c\u5b83\u4f1a\u8bfb\u53d6 skill \u6587\u4ef6\u81ea\u52a8\u5b8c\u6210\u914d\u7f6e\u3002',
    manual: '\u6216\u8005\u624b\u52a8\u5b89\u88c5',
  },
  footer: { tagline: '\u53d6\u540d\u81ea\u300a2001 \u592a\u7a7a\u6f2b\u6e38\u300b\u4e2d\u7684 TMA-1\u2014\u2014\u9759\u9ed8\u8bb0\u5f55\u4e00\u5207\uff0c\u7b49\u4f60\u6765\u6316\u6398\u3002' },
  ui: { copy: '\u590d\u5236', copied: '\u5df2\u590d\u5236\uff01', theme_light: '\u6d45\u8272', theme_dark: '\u6df1\u8272', theme_system: '\u8ddf\u968f\u7cfb\u7edf' },
};

export const es: T = {
  lang: 'es',
  title: 'TMA1 \u2014 Observabilidad Local para Agentes LLM',
  description: 'Observabilidad local para tus agentes de IA. Tokens, costos, latencia y replay de conversaciones \u2014 en tu m\u00e1quina.',
  nav: { features: 'Funcionalidades', how: 'C\u00f3mo funciona', security: 'Seguridad' },
  hero: {
    hook: 'Necesitaba saber cu\u00e1nto cuestan mis agentes \u2014 y si estaban haciendo algo peligroso.',
    h1_1: 'Sab\u00e9 qu\u00e9 hace tu agente',
    h1_2: 'y cu\u00e1nto te cuesta',
    subtitle: 'Tokens, costo, latencia \u2014 cada llamada LLM, registrada <em>localmente</em>. Hac\u00e9 clic en un pico para ver qu\u00e9 conversaci\u00f3n est\u00e1 quemando plata.',
  },
  onboarding: { label: 'ONBOARDING DEL AGENTE', manual: 'Instalaci\u00f3n manual' },
  highlights: [
    { title: 'Mir\u00e1 qu\u00e9 pas\u00f3', desc: 'Un agente corri\u00f3 20 minutos. \u00bfQu\u00e9 hizo? Abr\u00ed el dashboard, hac\u00e9 clic en un trace, le\u00e9 la conversaci\u00f3n completa.' },
    { title: 'Detect\u00e1 las llamadas caras', desc: 'Esa sesi\u00f3n de $40 del martes pasado \u2014 \u00bffue un context window enorme o un loop de reintentos? Ahora lo sab\u00e9s.' },
    { title: 'Nada sale de tu m\u00e1quina', desc: 'Tu agente ve tu c\u00f3digo, tus variables de entorno, tus claves. Esos datos se quedan en <code>~/.tma1/</code>, nunca se suben a ninguna parte.' },
  ],
  features: {
    kicker: 'Funcionalidades', title: 'Observabilidad sin complicaciones',
    desc: 'Siete vistas para Claude Code, Codex, Copilot CLI, OpenClaw, OTel GenAI, Sessions y Prompts. El dashboard elige la correcta según tus datos. Sin Grafana, sin nube, sin YAML.',
    cards: [
      { num: '01', title: 'Desglose de costos', desc: '\u00bfQu\u00e9 modelo cuesta m\u00e1s? \u00bfQu\u00e9 conversaci\u00f3n quem\u00f3 tu presupuesto? Tokens y costo estimado por modelo, m\u00e1s burn rate y ratios de cache hit.' },
      { num: '02', title: 'Detecci\u00f3n de anomal\u00edas', desc: 'Un agente en un loop de reintentos puede quemar cientos de d\u00f3lares. Cada vista de agente tiene una pesta\u00f1a Anomalies. Hac\u00e9 clic en cualquiera para saltar a esa sesi\u00f3n y ver qu\u00e9 sali\u00f3 mal.' },
      { num: '03', title: 'Sessions', desc: 'Tu agente corri\u00f3 25 minutos. \u00bfQu\u00e9 pas\u00f3? Abr\u00ed el overlay de sesi\u00f3n: a la izquierda la actividad de archivos, contexto y API calls. A la derecha, el timeline completo. O mir\u00e1 el canvas en vivo mientras tu agente trabaja.' },
      { num: '04', title: 'An\u00e1lisis de herramientas', desc: 'Cuando tu agente se siente lento, \u00bfes el modelo o las herramientas? p50 y p95 de latencia por herramienta, conteos de llamadas, tasas de \u00e9xito y l\u00edneas de tendencia.' },
      { num: '05', title: 'Monitoreo de seguridad', desc: 'Tu agente puede ejecutar comandos shell, hacer fetches a URLs externas y recibir prompts inyectados. TMA1 marca todo. Para OpenClaw tambi\u00e9n rastrea errores de webhook y sesiones atascadas.' },
      { num: '06', title: 'B\u00fasqueda de texto completo', desc: 'Escrib\u00ed una palabra clave en la pesta\u00f1a de b\u00fasqueda de Sessions y aparecen las conversaciones, herramientas y resultados que coinciden. Hac\u00e9 clic en un resultado para abrir la sesi\u00f3n en ese evento exacto.' },
      { num: '07', title: 'Cierra el loop del agente', desc: 'La observabilidad es la mitad. TMA1 tambi\u00e9n reinyecta lo que ve al loop del agente. Cinco hooks de Claude Code prependen un resumen de sesi\u00f3n, estado previo y anomal\u00edas en vivo al pr\u00f3ximo turno. Siete herramientas MCP le permiten al agente pedir estado de sesi\u00f3n, build, cambios externos o sesiones de agentes pares cuando las necesita. Seis reglas de anomal\u00eda se enrutan por canal \u2014 ediciones sobre vistas obsoletas y fallos de build repetidos bloquean <code>Stop</code>; las se\u00f1ales de menor prioridad llegan al pr\u00f3ximo user prompt. <code>/tma1-peer codex</code> trae al contexto de Claude lo que Codex acaba de dejar en este proyecto, sin saltar entre terminales.' },
    ],
  },
  how: {
    kicker: 'C\u00f3mo funciona', title: 'Configuraci\u00f3n',
    desc: 'Peg\u00e1 la instrucci\u00f3n de onboarding en tu agente y se encarga del resto. O hacelo vos:',
    steps: [
      { num: '[1]', title: 'Instalar', desc: 'Un comando. Todo se descarga en <code>~/.tma1/</code>. Sin Docker, sin paquetes del sistema.' },
      { num: '[2]', title: 'Configurar tu agente', desc: 'Apunt\u00e1 el endpoint OTel a <code>http://localhost:14318/v1/otlp</code>. Funciona con Claude Code, Codex, OpenClaw o cualquier SDK OTel. GitHub Copilot CLI no necesita configuraci\u00f3n \u2014 TMA1 detecta sus logs de sesi\u00f3n autom\u00e1ticamente.' },
      { num: '[3]', title: 'Abrir el dashboard', desc: 'Abr\u00ed <code>localhost:14318</code> en el navegador. Los traces aparecen segundos despu\u00e9s de la siguiente llamada LLM.' },
    ],
  },
  security: {
    kicker: 'Seguridad', title: 'Seguridad y privacidad',
    desc: 'Tu agente lee tu c\u00f3digo, tus API keys, tu infraestructura. Mandar eso a un servicio de observabilidad en la nube anula el prop\u00f3sito. Todo se queda local.',
    panel_title: 'C\u00f3mo se almacenan los datos',
    panel_body: 'TMA1 guarda traces y logs de conversaci\u00f3n en tu disco local, en <code>~/.tma1/data/</code>. No se sube nada a servicios remotos y pod\u00e9s inspeccionar o borrar los datos cuando quieras.',
    cards: [
      { title: 'Sin llamadas de red', desc: 'Tras el primer inicio (que descarga el motor de base de datos integrado una sola vez), TMA1 no hace m\u00e1s llamadas de red. Sin anal\u00edticas, sin reportes de error, sin chequeos de actualizaci\u00f3n.' },
      { title: 'Completamente open source', desc: 'TMA1 usa licencia Apache-2.0. Le\u00e9 el c\u00f3digo, audit\u00e1 el build y corr\u00e9lo sin conexi\u00f3n.' },
      { title: 'Un solo binario', desc: '<code>tma1-server</code> corre como un \u00fanico proceso local y administra su motor de almacenamiento integrado. Sin Docker, sin paquetes del sistema, sin dependencias runtime.' },
      { title: 'Tus datos, tu disco', desc: 'Borr\u00e1 <code>~/.tma1/</code> y todo desaparece. Sin estado hu\u00e9rfano en la nube, sin cuentas remotas que cerrar.' },
    ],
  },
  faq: {
    kicker: 'FAQ', title: 'Preguntas frecuentes',
    items: [
      { q: '\u00bfQu\u00e9 agentes soporta?', a: 'Cualquier agente que emita datos OpenTelemetry, m\u00e1s algunos v\u00eda auto-descubrimiento de JSONL. Claude Code env\u00eda m\u00e9tricas y logs. Codex env\u00eda logs y m\u00e9tricas, y los archivos JSONL de sesi\u00f3n se analizan autom\u00e1ticamente para la reproducci\u00f3n de conversaciones. GitHub Copilot CLI no requiere configuraci\u00f3n: sus logs de sesi\u00f3n en <code>~/.copilot/session-state/</code> se detectan autom\u00e1ticamente. OpenClaw env\u00eda traces y m\u00e9tricas, y los archivos JSONL de sesi\u00f3n se analizan autom\u00e1ticamente. Cualquier SDK OTel con convenciones sem\u00e1nticas GenAI funciona de entrada. El dashboard detecta autom\u00e1ticamente la fuente de datos y muestra la vista correspondiente.' },
      { q: '\u00bfSe pueden consultar los datos con SQL?', a: 'S\u00ed. Ejecut\u00e1 <code>mysql -h 127.0.0.1 -P 14002</code> para conectarte al endpoint SQL local, o abr\u00ed <code><a href="http://localhost:14000/dashboard/">localhost:14000/dashboard/</a></code> para la interfaz de consultas. Traces en <code>opentelemetry_traces</code>, logs en <code>opentelemetry_logs</code>, datos de sesi\u00f3n en <code>tma1_hook_events</code> y <code>tma1_messages</code>, y las m\u00e9tricas OTel crean tablas autom\u00e1ticamente.' },
      { q: '\u00bfCu\u00e1nto disco ocupa?', a: 'Depende de la actividad del agente y del largo de las conversaciones. En un uso t\u00edpico, unos cientos de MB por mes.' },
    ],
  },
  quickstart: {
    kicker: 'Inicio r\u00e1pido', title: 'Prob\u00e1lo ahora',
    desc: 'Peg\u00e1 esto en tu agente. Lee el archivo de skill y se encarga del resto.',
    manual: 'O instal\u00e1 manualmente',
  },
  footer: { tagline: 'Nombrado como TMA-1 de <em>2001: Una odisea del espacio</em>. Registrando todo en silencio hasta que lo descubras.' },
  ui: { copy: 'Copiar', copied: '\u00a1Copiado!', theme_light: 'Claro', theme_dark: 'Oscuro', theme_system: 'Sistema' },
};

export const locales = { en, zh, es } as const;
export type Locale = keyof typeof locales;
