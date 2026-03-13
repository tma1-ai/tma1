export interface T {
  lang: string;
  title: string;
  description: string;
  nav: { features: string; how: string; security: string };
  hero: { h1_1: string; h1_2: string; subtitle: string };
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
    h1_1: 'Know what your agent is doing',
    h1_2: 'and what it costs',
    subtitle: 'Local observability for your AI agents. Every LLM call \u2014 tokens, cost, latency \u2014 silently recorded on your machine.',
  },
  onboarding: { label: 'AGENT ONBOARDING', manual: 'Manual install' },
  highlights: [
    { title: 'Inspect every call', desc: 'Click any LLM call to see tokens, cost, latency, and \u2014 where supported \u2014 the full prompt and response.' },
    { title: 'Track the cost', desc: 'Token usage and cost by model, over time. Spot the expensive calls.' },
    { title: 'Runs on your machine', desc: 'All data in <code>~/.tma1/</code>. No cloud, no accounts, no external services.' },
  ],
  features: {
    kicker: 'Features', title: 'Observability without the overhead',
    desc: 'Cost, latency, security, and errors \u2014 in one local dashboard.',
    cards: [
      { num: '01', title: 'Cost breakdown', desc: 'Token counts and estimated cost per model, tracked over time. See which models and conversations cost the most, with burn-rate projections and cache efficiency analysis.' },
      { num: '02', title: 'Latency tracking', desc: 'p50 and p95 latency percentiles for every model. Tool performance tables show call counts, success rates, and average response times.' },
      { num: '03', title: 'Security monitoring', desc: 'Detects shell commands, external fetches, and potential prompt injections in GenAI traces. Tracks webhook errors, stuck sessions, and channel health for OpenClaw.' },
      { num: '04', title: 'Conversation replay', desc: 'For agents that emit conversation content (like Claude Code), click a trace to read the actual dialogue \u2014 handy for debugging or auditing what your agent said.' },
      { num: '05', title: 'Anomaly detection', desc: 'Flags calls with unusual token counts, high error rates, or slow responses. Catch runaway loops before they pile up cost.' },
      { num: '06', title: 'Full-text search', desc: 'Search across all recorded events and traces. Find a specific model, trace an error back to a call, or filter by tool name.' },
    ],
  },
  how: {
    kicker: 'How it works', title: 'Setup',
    desc: 'Paste the onboarding instruction into your agent and it handles the rest. Or do it yourself:',
    steps: [
      { num: '[1]', title: 'Install', desc: 'One command. Downloads everything into <code>~/.tma1/</code>. No Docker, no system packages.' },
      { num: '[2]', title: 'Configure your agent', desc: 'Point the OTel endpoint to <code>http://localhost:14318/v1/otlp</code>. Works with Claude Code, Codex, OpenClaw, or any OTel SDK.' },
      { num: '[3]', title: 'Open the dashboard', desc: 'Browse to <code>localhost:14318</code>. Traces show up seconds after your agent\u2019s next LLM call.' },
    ],
  },
  security: {
    kicker: 'Security', title: 'Security & Privacy',
    desc: 'Agent conversations often contain sensitive context. TMA1 keeps everything on your machine.',
    panel_title: 'How data is stored',
    panel_body: 'TMA1 stores traces and conversation logs on your local disk in <code>~/.tma1/data/</code>. Nothing is uploaded to remote services, and you can inspect or delete the data at any time.',
    cards: [
      { title: 'No network calls', desc: 'After install, TMA1 doesn\u2019t contact any external service. All assets are bundled locally. No analytics, no crash reports, no update checks.' },
      { title: 'Fully open source', desc: 'TMA1 is Apache-2.0. Read the code, audit the build, and run it air-gapped.' },
      { title: 'Single binary', desc: '<code>tma1-server</code> runs as one local process and manages its embedded storage engine. No Docker, no system packages, no runtime dependencies.' },
      { title: 'Your data, your disk', desc: 'Delete <code>~/.tma1/</code> and everything is gone. No orphaned cloud state, no remote accounts to close.' },
    ],
  },
  faq: {
    kicker: 'FAQ', title: 'Common questions',
    items: [
      { q: 'Which agents are supported?', a: 'Any agent that emits OpenTelemetry data. Claude Code sends metrics and logs. Codex sends logs and traces, and can also emit native metrics when <code>otel.metrics_exporter</code> is configured. OpenClaw sends traces and metrics. Any OTel SDK app with GenAI semantic conventions works out of the box. The dashboard auto-detects the data source and shows the right view.' },
      { q: 'Can I query the data with SQL?', a: 'Yes. Run <code>mysql -h 127.0.0.1 -P 14002</code> to connect to the local SQL endpoint, or open <code><a href="http://localhost:14000/dashboard/">localhost:14000/dashboard/</a></code> for the built-in query UI. Raw traces are in <code>opentelemetry_traces</code>, logs in <code>opentelemetry_logs</code>, and native metric tables are auto-created from incoming OTel metrics.' },
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
    h1_1: '\u4f60\u7684 agent \u5728\u505a\u4ec0\u4e48',
    h1_2: '\u82b1\u4e86\u591a\u5c11\u94b1',
    subtitle: 'AI agent \u7684\u672c\u5730\u53ef\u89c2\u6d4b\u65b9\u6848\u3002\u6bcf\u6b21 LLM \u8c03\u7528\u7684 token\u3001\u8d39\u7528\u3001\u5ef6\u8fdf\u2014\u2014\u9759\u9ed8\u8bb0\u5f55\u5728\u4f60\u7684\u673a\u5668\u4e0a\u3002',
  },
  onboarding: { label: 'AGENT \u63a5\u5165', manual: '\u624b\u52a8\u5b89\u88c5' },
  highlights: [
    { title: '\u68c0\u89c6\u6bcf\u6b21\u8c03\u7528', desc: '\u70b9\u51fb\u4efb\u610f\u4e00\u6b21 LLM \u8c03\u7528\uff0c\u67e5\u770b token\u3001\u8d39\u7528\u3001\u5ef6\u8fdf\uff0c\u4ee5\u53ca\uff08\u5982\u679c agent \u652f\u6301\uff09\u5b8c\u6574\u7684 prompt \u548c response\u3002' },
    { title: '\u8ffd\u8e2a\u8d39\u7528', desc: '\u6309\u6a21\u578b\u3001\u6309\u65f6\u95f4\u6bb5\u7edf\u8ba1 token \u7528\u91cf\u548c\u8d39\u7528\u3002\u4e00\u773c\u770b\u51fa\u54ea\u4e9b\u8c03\u7528\u6700\u8d35\u3002' },
    { title: '\u6570\u636e\u4e0d\u51fa\u672c\u673a', desc: '\u6240\u6709\u6570\u636e\u5b58\u5728 <code>~/.tma1/</code>\u3002\u4e0d\u9700\u8981\u4e91\u8d26\u53f7\uff0c\u4e0d\u9700\u8981\u5916\u90e8\u670d\u52a1\u3002' },
  ],
  features: {
    kicker: '\u529f\u80fd', title: '\u8f7b\u91cf\u53ef\u89c2\u6d4b',
    desc: '\u8d39\u7528\u3001\u5ef6\u8fdf\u3001\u5b89\u5168\u3001\u9519\u8bef\u2014\u2014\u4e00\u4e2a\u672c\u5730 dashboard \u641e\u5b9a\u3002',
    cards: [
      { num: '01', title: '\u8d39\u7528\u660e\u7ec6', desc: '\u6309\u6a21\u578b\u8ffd\u8e2a token \u7528\u91cf\u548c\u9884\u4f30\u8d39\u7528\u3002\u54ea\u4e9b\u6a21\u578b\u548c\u5bf9\u8bdd\u6700\u70e7\u94b1\uff0c\u4e00\u76ee\u4e86\u7136\u3002\u652f\u6301 burn rate \u9884\u6d4b\u548c\u7f13\u5b58\u6548\u7387\u5206\u6790\u3002' },
      { num: '02', title: '\u5ef6\u8fdf\u8ffd\u8e2a', desc: '\u6bcf\u4e2a\u6a21\u578b\u7684 p50 \u548c p95 \u5ef6\u8fdf\u5206\u4f4d\u6570\u3002\u5de5\u5177\u6027\u80fd\u8868\u663e\u793a\u8c03\u7528\u6b21\u6570\u3001\u6210\u529f\u7387\u548c\u5e73\u5747\u54cd\u5e94\u65f6\u95f4\u3002' },
      { num: '03', title: '\u5b89\u5168\u76d1\u63a7', desc: '\u68c0\u6d4b GenAI traces \u4e2d\u7684 shell \u547d\u4ee4\u3001\u5916\u90e8\u8bf7\u6c42\u548c\u6f5c\u5728\u7684 prompt \u6ce8\u5165\u3002\u8ffd\u8e2a OpenClaw \u7684 webhook \u9519\u8bef\u3001\u5361\u4f4f\u7684 session \u548c channel \u5065\u5eb7\u72b6\u6001\u3002' },
      { num: '04', title: '\u5bf9\u8bdd\u56de\u653e', desc: '\u5bf9\u4e8e\u53d1\u9001\u5bf9\u8bdd\u5185\u5bb9\u7684 agent\uff08\u5982 Claude Code\uff09\uff0c\u70b9\u51fb\u67d0\u6761 trace \u5c31\u80fd\u770b\u5230\u5b8c\u6574\u5bf9\u8bdd\u2014\u2014\u6392\u67e5\u95ee\u9898\u6216\u5ba1\u8ba1 agent \u8f93\u51fa\u65f6\u5f88\u65b9\u4fbf\u3002' },
      { num: '05', title: '\u5f02\u5e38\u68c0\u6d4b', desc: '\u6807\u8bb0 token \u5f02\u5e38\u504f\u9ad8\u3001\u9519\u8bef\u7387\u98d9\u5347\u6216\u54cd\u5e94\u53d8\u6162\u7684\u8c03\u7528\u3002\u5728\u8d39\u7528\u5806\u8d77\u6765\u4e4b\u524d\u6293\u4f4f\u5931\u63a7\u7684\u5faa\u73af\u3002' },
      { num: '06', title: '\u5168\u6587\u641c\u7d22', desc: '\u641c\u7d22\u6240\u6709\u8bb0\u5f55\u7684\u4e8b\u4ef6\u548c traces\u3002\u6309\u6a21\u578b\u8fc7\u6ee4\u3001\u8ffd\u8e2a\u9519\u8bef\u6765\u6e90\uff0c\u6216\u6309\u5de5\u5177\u540d\u67e5\u627e\u3002' },
    ],
  },
  how: {
    kicker: '\u5de5\u4f5c\u539f\u7406', title: '\u5b89\u88c5\u914d\u7f6e',
    desc: '\u628a\u63a5\u5165\u6307\u4ee4\u7c98\u8d34\u7ed9\u4f60\u7684 agent\uff0c\u5b83\u4f1a\u81ea\u52a8\u641e\u5b9a\u3002\u6216\u8005\u624b\u52a8\u6765\uff1a',
    steps: [
      { num: '[1]', title: '\u5b89\u88c5', desc: '\u4e00\u6761\u547d\u4ee4\uff0c\u6240\u6709\u6587\u4ef6\u88c5\u8fdb <code>~/.tma1/</code>\u3002\u4e0d\u9700\u8981 Docker\uff0c\u4e0d\u9700\u8981\u88c5\u522b\u7684\u3002' },
      { num: '[2]', title: '\u914d\u7f6e\u4f60\u7684 agent', desc: '\u5c06 OTel endpoint \u6307\u5411 <code>http://localhost:14318/v1/otlp</code>\u3002\u652f\u6301 Claude Code\u3001Codex\u3001OpenClaw \u6216\u4efb\u4f55 OTel SDK\u3002' },
      { num: '[3]', title: '\u6253\u5f00 dashboard', desc: '\u6d4f\u89c8\u5668\u6253\u5f00 <code>localhost:14318</code>\u3002agent \u4e0b\u6b21\u8c03 LLM \u540e\u51e0\u79d2\u5c31\u80fd\u770b\u5230 trace\u3002' },
    ],
  },
  security: {
    kicker: '\u5b89\u5168', title: '\u5b89\u5168\u4e0e\u9690\u79c1',
    desc: 'Agent \u5bf9\u8bdd\u7ecf\u5e38\u6d89\u53ca\u654f\u611f\u4fe1\u606f\u3002TMA1 \u628a\u4e00\u5207\u90fd\u7559\u5728\u4f60\u7684\u673a\u5668\u4e0a\u3002',
    panel_title: '\u6570\u636e\u600e\u4e48\u5b58\u7684',
    panel_body: 'TMA1 \u4f1a\u628a trace \u548c\u5bf9\u8bdd\u65e5\u5fd7\u4fdd\u5b58\u5728\u672c\u5730 <code>~/.tma1/data/</code>\u3002\u6570\u636e\u4e0d\u4f1a\u4e0a\u4f20\u5230\u4efb\u4f55\u8fdc\u7a0b\u670d\u52a1\uff0c\u4f60\u53ef\u4ee5\u968f\u65f6\u67e5\u770b\u6216\u5220\u9664\u3002',
    cards: [
      { title: '\u96f6\u7f51\u7edc\u8bf7\u6c42', desc: '\u5b89\u88c5\u5b8c\u6210\u540e\uff0cTMA1 \u4e0d\u8054\u7cfb\u4efb\u4f55\u5916\u90e8\u670d\u52a1\u3002\u6240\u6709\u8d44\u6e90\u5df2\u6253\u5305\u5728\u672c\u5730\u3002\u6ca1\u6709\u6570\u636e\u4e0a\u62a5\uff0c\u6ca1\u6709\u5d29\u6e83\u62a5\u544a\uff0c\u6ca1\u6709\u66f4\u65b0\u68c0\u67e5\u3002' },
      { title: '\u5b8c\u5168\u5f00\u6e90', desc: 'TMA1 \u91c7\u7528 Apache-2.0\u3002\u4ee3\u7801\u53ef\u5ba1\u8ba1\uff0c\u6784\u5efa\u53ef\u68c0\u67e5\uff0c\u652f\u6301\u79bb\u7ebf\u8fd0\u884c\u3002' },
      { title: '\u5355\u4e00\u4e8c\u8fdb\u5236', desc: '<code>tma1-server</code> \u4ee5\u5355\u8fdb\u7a0b\u672c\u5730\u8fd0\u884c\uff0c\u5e76\u7ba1\u7406\u5185\u7f6e\u5b58\u50a8\u5f15\u64ce\u3002\u4e0d\u8981 Docker\uff0c\u4e0d\u8981\u7cfb\u7edf\u5305\uff0c\u6ca1\u6709\u8fd0\u884c\u65f6\u4f9d\u8d56\u3002' },
      { title: '\u4f60\u7684\u6570\u636e\uff0c\u4f60\u7684\u78c1\u76d8', desc: '\u5220\u6389 <code>~/.tma1/</code> \u5c31\u5168\u6ca1\u4e86\u3002\u6ca1\u6709\u6b8b\u7559\u7684\u4e91\u7aef\u72b6\u6001\uff0c\u6ca1\u6709\u8981\u6ce8\u9500\u7684\u8fdc\u7a0b\u8d26\u53f7\u3002' },
    ],
  },
  faq: {
    kicker: 'FAQ', title: '\u5e38\u89c1\u95ee\u9898',
    items: [
      { q: '\u652f\u6301\u54ea\u4e9b agent\uff1f', a: '\u4efb\u4f55\u53d1\u9001 OpenTelemetry \u6570\u636e\u7684 agent\u3002Claude Code \u53d1\u9001 metrics \u548c logs\uff0cCodex \u53d1\u9001 logs \u548c traces\uff0c\u914d\u7f6e <code>otel.metrics_exporter</code> \u540e\u4e5f\u53ef\u4ee5\u53d1\u9001\u539f\u751f metrics\uff0cOpenClaw \u53d1\u9001 traces \u548c metrics\uff0c\u4efb\u4f55 GenAI \u8bed\u4e49\u89c4\u8303\u7684 OTel SDK \u5e94\u7528\u4e5f\u652f\u6301\u3002Dashboard \u81ea\u52a8\u68c0\u6d4b\u6570\u636e\u6e90\u5e76\u5c55\u793a\u5bf9\u5e94\u89c6\u56fe\u3002' },
      { q: '\u80fd\u76f4\u63a5\u7528 SQL \u67e5\u5417\uff1f', a: '\u80fd\u3002\u8fd0\u884c <code>mysql -h 127.0.0.1 -P 14002</code> \u8fde\u63a5\u672c\u5730 SQL \u7aef\u53e3\uff0c\u6216\u6253\u5f00 <code><a href="http://localhost:14000/dashboard/">localhost:14000/dashboard/</a></code> \u4f7f\u7528\u5185\u7f6e\u67e5\u8be2\u754c\u9762\u3002\u539f\u59cb traces \u5728 <code>opentelemetry_traces</code>\uff0clogs \u5728 <code>opentelemetry_logs</code>\uff0cOTel metrics \u4f1a\u81ea\u52a8\u521b\u8868\u5b58\u50a8\u3002' },
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
    h1_1: 'Sab\u00e9 qu\u00e9 hace tu agente',
    h1_2: 'y cu\u00e1nto te cuesta',
    subtitle: 'Observabilidad local para tus agentes de IA. Cada llamada LLM \u2014 tokens, costo, latencia \u2014 registrada silenciosamente en tu m\u00e1quina.',
  },
  onboarding: { label: 'ONBOARDING DEL AGENTE', manual: 'Instalaci\u00f3n manual' },
  highlights: [
    { title: 'Inspeccion\u00e1 cada llamada', desc: 'Hac\u00e9 clic en cualquier llamada LLM para ver tokens, costo, latencia y \u2014 si el agente lo soporta \u2014 el prompt y la respuesta completos.' },
    { title: 'Rastre\u00e1 los costos', desc: 'Uso de tokens y costo por modelo, a lo largo del tiempo. Encontr\u00e1 r\u00e1pido las llamadas m\u00e1s caras.' },
    { title: 'Todo en tu m\u00e1quina', desc: 'Todos los datos en <code>~/.tma1/</code>. Sin cuentas en la nube, sin servicios externos.' },
  ],
  features: {
    kicker: 'Funcionalidades', title: 'Observabilidad sin complicaciones',
    desc: 'Costos, latencia, seguridad y errores \u2014 en un dashboard local.',
    cards: [
      { num: '01', title: 'Desglose de costos', desc: 'Tokens y costo estimado por modelo, rastreados a lo largo del tiempo. De un vistazo, qu\u00e9 modelos y conversaciones cuestan m\u00e1s. Con proyecciones de burn rate y an\u00e1lisis de eficiencia de cach\u00e9.' },
      { num: '02', title: 'Seguimiento de latencia', desc: 'Percentiles p50 y p95 de latencia por modelo. Tablas de rendimiento de herramientas con conteo de llamadas, tasa de \u00e9xito y tiempos de respuesta.' },
      { num: '03', title: 'Monitoreo de seguridad', desc: 'Detecta comandos shell, peticiones externas e inyecciones de prompt potenciales en traces GenAI. Rastrea errores de webhook, sesiones atascadas y salud de canales en OpenClaw.' },
      { num: '04', title: 'Replay de conversaciones', desc: 'Para agentes que emiten contenido de conversaci\u00f3n (como Claude Code), hac\u00e9 clic en un trace para leer el di\u00e1logo \u2014 \u00fatil para depurar o auditar lo que dijo tu agente.' },
      { num: '05', title: 'Detecci\u00f3n de anomal\u00edas', desc: 'Marca llamadas con tokens inusualmente altos, errores elevados o respuestas lentas. Detect\u00e1 bucles descontrolados antes de que acumulen costos.' },
      { num: '06', title: 'B\u00fasqueda de texto completo', desc: 'Busc\u00e1 en todos los eventos y traces. Filtr\u00e1 por modelo, rastre\u00e1 un error hasta la llamada original, o busc\u00e1 por nombre de herramienta.' },
    ],
  },
  how: {
    kicker: 'C\u00f3mo funciona', title: 'Configuraci\u00f3n',
    desc: 'Peg\u00e1 la instrucci\u00f3n de onboarding en tu agente y se encarga del resto. O hacelo vos:',
    steps: [
      { num: '[1]', title: 'Instalar', desc: 'Un comando. Todo se descarga en <code>~/.tma1/</code>. Sin Docker, sin paquetes del sistema.' },
      { num: '[2]', title: 'Configurar tu agente', desc: 'Apunt\u00e1 el endpoint OTel a <code>http://localhost:14318/v1/otlp</code>. Funciona con Claude Code, Codex, OpenClaw o cualquier SDK OTel.' },
      { num: '[3]', title: 'Abrir el dashboard', desc: 'Abr\u00ed <code>localhost:14318</code> en el navegador. Los traces aparecen segundos despu\u00e9s de la siguiente llamada LLM.' },
    ],
  },
  security: {
    kicker: 'Seguridad', title: 'Seguridad y privacidad',
    desc: 'Las conversaciones de agentes suelen tener contexto sensible. TMA1 mantiene todo en tu m\u00e1quina.',
    panel_title: 'C\u00f3mo se almacenan los datos',
    panel_body: 'TMA1 guarda traces y logs de conversaci\u00f3n en tu disco local, en <code>~/.tma1/data/</code>. No se sube nada a servicios remotos y pod\u00e9s inspeccionar o borrar los datos cuando quieras.',
    cards: [
      { title: 'Sin llamadas de red', desc: 'Despu\u00e9s de instalar, TMA1 no contacta ning\u00fan servicio externo. Todos los recursos est\u00e1n empaquetados localmente. Sin anal\u00edticas, sin reportes de error, sin chequeos de actualizaci\u00f3n.' },
      { title: 'Completamente open source', desc: 'TMA1 usa licencia Apache-2.0. Le\u00e9 el c\u00f3digo, audit\u00e1 el build y corr\u00e9lo sin conexi\u00f3n.' },
      { title: 'Un solo binario', desc: '<code>tma1-server</code> corre como un \u00fanico proceso local y administra su motor de almacenamiento integrado. Sin Docker, sin paquetes del sistema, sin dependencias runtime.' },
      { title: 'Tus datos, tu disco', desc: 'Borr\u00e1 <code>~/.tma1/</code> y todo desaparece. Sin estado hu\u00e9rfano en la nube, sin cuentas remotas que cerrar.' },
    ],
  },
  faq: {
    kicker: 'FAQ', title: 'Preguntas frecuentes',
    items: [
      { q: '\u00bfQu\u00e9 agentes soporta?', a: 'Cualquier agente que emita datos OpenTelemetry. Claude Code env\u00eda m\u00e9tricas y logs. Codex env\u00eda logs y traces, y tambi\u00e9n puede emitir m\u00e9tricas nativas cuando se configura <code>otel.metrics_exporter</code>. OpenClaw env\u00eda traces y m\u00e9tricas. Cualquier SDK OTel con convenciones sem\u00e1nticas GenAI funciona de entrada. El dashboard detecta autom\u00e1ticamente la fuente de datos y muestra la vista correspondiente.' },
      { q: '\u00bfSe pueden consultar los datos con SQL?', a: 'S\u00ed. Ejecut\u00e1 <code>mysql -h 127.0.0.1 -P 14002</code> para conectarte al endpoint SQL local, o abr\u00ed <code><a href="http://localhost:14000/dashboard/">localhost:14000/dashboard/</a></code> para usar la interfaz de consultas integrada. Los traces crudos est\u00e1n en <code>opentelemetry_traces</code>, los logs en <code>opentelemetry_logs</code>, y las m\u00e9tricas OTel se guardan en tablas nativas creadas autom\u00e1ticamente.' },
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
