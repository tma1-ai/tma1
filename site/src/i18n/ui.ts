import type { Locale } from './products';

export interface T {
  lang: Locale;
  title: string;
  description: string;
  nav: { features: string; how: string; security: string };
  hero: { h1_1: string; h1_2: string; subtitle: string; cta: string; overview: string; caption: string };
  demo: string;
  features: { title: string; cards: Array<{ id: string; title: string; desc: string }> };
  video: string;
  context: { title: string; cards: Array<{ title: string; desc: string }> };
  how: { title: string; label: string; manual: string; opencode: string; note: string; otel: string };
  security: { title: string; desc: string; link: string };
  footer: { tagline: string };
  ui: { copy: string; copied: string; failed: string };
}

export const en: T = {
  "lang": "en",
  "title": "TMA1 — local-first agent observability",
  "description": "Track Claude Code, Codex, Copilot CLI, and OpenCode in one local dashboard. Compare sessions, inspect tool outcomes, and search conversation history.",
  "nav": {
    "features": "Explore",
    "how": "Install",
    "security": "Privacy"
  },
  "hero": {
    "h1_1": "Close the loop",
    "h1_2": "around your agent.",
    "subtitle": "Sessions, tool calls, and token usage in one local workspace. Inspect what happened, compare runs, and feed observations back into the agent.",
    "cta": "Get started",
    "overview": "One view across agents",
    "caption": "Overview · activity, recent sessions, and recorded tool outcomes"
  },
  "demo": "Screenshots and video use demo data.",
  "features": {
    "title": "From activity to the details",
    "cards": [
      {
        "id": "sessions",
        "title": "Session replay",
        "desc": "Follow the conversation alongside tool calls and file activity. Search recorded messages and open the matching event."
      },
      {
        "id": "comparison",
        "title": "Compare sessions",
        "desc": "Compare two sessions across agents: reported tokens, cache read share, tool failures, and compaction events. Missing usage stays unavailable."
      },
      {
        "id": "tools",
        "title": "Tool outcomes",
        "desc": "Overview summarizes recorded tool completions and failures across agents. Claude Code adds tool latency, success rates, and failure details from its telemetry."
      },
      {
        "id": "cost",
        "title": "Tokens & cost",
        "desc": "Inspect token usage, cache activity, and model costs where telemetry provides them. Estimates depend on available usage and pricing; they are not billing totals."
      },
      {
        "id": "anomaly",
        "title": "Anomalies",
        "desc": "Review repeated build or test failures, stale file views, external changes, and large input-usage samples. Findings provide evidence and a suggested next step."
      },
      {
        "id": "security",
        "title": "Security signals",
        "desc": "Inspect recorded commands, URL requests, and pattern-based risk flags in GenAI traces. Flags are review signals, not a security verdict."
      }
    ]
  },
  "video": "Watch a session walkthrough",
  "context": {
    "title": "Observations the agent can read",
    "cards": [
      {
        "title": "Context for the next turn",
        "desc": "Claude Code and Codex hooks provide session context and advisory findings. Prompt review can use a configured model to suggest changes backed by conversation excerpts."
      },
      {
        "title": "Search across sessions",
        "desc": "MCP tools and the /tma1-peer and /tma1-search skills retrieve earlier conversations, including work from other agents on the same project."
      }
    ]
  },
  "how": {
    "title": "Run locally",
    "label": "Paste this instruction into Claude Code or Codex",
    "manual": "Manual installation",
    "opencode": "OpenCode setup →",
    "otel": "OpenTelemetry SDKs: send OTLP/HTTP to http://localhost:14318/v1/otlp. GenAI traces use the OpenTelemetry GenAI semantic conventions.",
    "note": "OpenCode imports sessions through a plugin or an exported snapshot. Import does not install hooks or inject context. Copilot CLI session logs are discovered automatically."
  },
  "security": {
    "title": "Local storage. Explicit model access.",
    "desc": "Traces and conversations are stored in ~/.tma1/data/, backed by GreptimeDB. Optional model reviews send selected prompts and conversation excerpts to the configured provider.",
    "link": "Read the integration docs →"
  },
  "footer": {
    "tagline": "TMA1 · Local-first agent observability. Apache-2.0."
  },
  "ui": {
    "copy": "Copy",
    "copied": "Copied",
    "failed": "Copy failed; select the text to copy."
  }
};

export const zh: T = {
  "lang": "zh",
  "title": "TMA1 — 本地优先的 agent 可观测性",
  "description": "在本地统一查看 Claude Code、Codex、Copilot CLI 和 OpenCode。对比会话、检查工具调用结果、检索历史对话。",
  "nav": {
    "features": "功能",
    "how": "安装",
    "security": "隐私"
  },
  "hero": {
    "h1_1": "观测 agent，",
    "h1_2": "让反馈进入下一轮。",
    "subtitle": "在同一个本地工作区查看会话、工具调用和 token 用量，回溯执行过程、对比不同会话，并把观测结果送回 agent。",
    "cta": "开始使用",
    "overview": "跨 agent 的统一视图",
    "caption": "Overview · 活动趋势、最近会话与已记录的工具调用结果"
  },
  "demo": "截图与视频使用演示数据。",
  "features": {
    "title": "从整体活动到执行细节",
    "cards": [
      {
        "id": "sessions",
        "title": "会话回放",
        "desc": "沿时间线查看对话、工具调用和文件活动。检索已记录的消息，直接跳转到匹配事件。"
      },
      {
        "id": "comparison",
        "title": "会话对比",
        "desc": "跨 agent 对比两次会话的上报 token、缓存读取占比、工具失败和上下文压缩事件。缺失的用量标记为不可用。"
      },
      {
        "id": "tools",
        "title": "工具调用结果",
        "desc": "Overview 汇总各 agent 已记录的工具完成与失败次数。Claude Code 视图还展示遥测中的工具延迟、成功率和失败详情。"
      },
      {
        "id": "cost",
        "title": "用量与费用",
        "desc": "根据遥测数据查看 token 用量、缓存活动和模型费用。估算依赖可用的用量与价格数据，不等同于账单金额。"
      },
      {
        "id": "anomaly",
        "title": "异常检测",
        "desc": "查看重复的构建或测试失败、过期文件视图、外部变更和较大的输入用量样本。检测结果包含证据和下一步建议。"
      },
      {
        "id": "security",
        "title": "安全信号",
        "desc": "检查 GenAI trace 中记录的命令、URL 请求和基于模式匹配的风险标记。这些标记供人工审查，不是安全判定。"
      }
    ]
  },
  "video": "查看会话演示",
  "context": {
    "title": "agent 也能读取观测结果",
    "cards": [
      {
        "title": "下一轮的上下文",
        "desc": "Claude Code 和 Codex hooks 提供会话上下文与异常提示。提示词审阅可调用配置的模型，根据对话片段提出有证据支持的修改建议。"
      },
      {
        "title": "跨会话检索",
        "desc": "MCP 工具及 /tma1-peer、/tma1-search skills 可检索历史对话，包括其他 agent 在同一项目中的工作。"
      }
    ]
  },
  "how": {
    "title": "在本地运行",
    "label": "将这条指令粘贴到 Claude Code 或 Codex",
    "manual": "手动安装",
    "opencode": "OpenCode 接入说明 →",
    "otel": "OpenTelemetry SDK 可将 OTLP/HTTP 数据发送到 http://localhost:14318/v1/otlp。GenAI trace 使用 OpenTelemetry GenAI 语义规范。",
    "note": "OpenCode 通过插件或导出快照导入会话，不安装 hooks，也不注入上下文。Copilot CLI 的会话日志会被自动发现。"
  },
  "security": {
    "title": "本地存储，按需调用模型。",
    "desc": "Trace 和对话通过 GreptimeDB 存储在 ~/.tma1/data/。可选的模型审阅会将选中的提示词及对话片段发送到配置的提供商。",
    "link": "查看接入文档 →"
  },
  "footer": {
    "tagline": "TMA1 · 本地优先的 agent 可观测性。Apache-2.0。"
  },
  "ui": {
    "copy": "复制",
    "copied": "已复制",
    "failed": "复制失败，请选中文本复制。"
  }
};

export const es: T = {
  "lang": "es",
  "title": "TMA1 — observabilidad local para agentes",
  "description": "Claude Code, Codex, Copilot CLI y OpenCode en un dashboard local. Compara sesiones, revisa resultados de herramientas y busca conversaciones.",
  "nav": {
    "features": "Explorar",
    "how": "Instalar",
    "security": "Privacidad"
  },
  "hero": {
    "h1_1": "Cierra el ciclo",
    "h1_2": "de tu agente.",
    "subtitle": "Sesiones, herramientas y uso de tokens en un espacio local. Revisa lo ocurrido, compara sesiones y devuelve las observaciones al agente.",
    "cta": "Empezar",
    "overview": "Una vista entre agentes",
    "caption": "Overview · actividad, sesiones recientes y resultados registrados de herramientas"
  },
  "demo": "Las capturas y el video usan datos de demostración.",
  "features": {
    "title": "De la actividad a los detalles",
    "cards": [
      {
        "id": "sessions",
        "title": "Reproducir sesiones",
        "desc": "Revisa la conversación junto con las herramientas y la actividad de archivos. Busca mensajes registrados y abre el evento correspondiente."
      },
      {
        "id": "comparison",
        "title": "Comparar sesiones",
        "desc": "Compara dos sesiones entre agentes: tokens reportados, proporción de lecturas de caché, fallos de herramientas y compactaciones. El uso ausente queda sin datos."
      },
      {
        "id": "tools",
        "title": "Resultados de herramientas",
        "desc": "Overview resume herramientas completadas y fallos registrados entre agentes. Claude Code añade latencia, tasas de éxito y detalles de fallos de su telemetría."
      },
      {
        "id": "cost",
        "title": "Tokens y costos",
        "desc": "Revisa tokens, actividad de caché y costos por modelo cuando la telemetría los proporciona. Las estimaciones dependen del uso y los precios disponibles; no son totales de facturación."
      },
      {
        "id": "anomaly",
        "title": "Anomalías",
        "desc": "Revisa fallos repetidos de compilación o pruebas, vistas obsoletas de archivos, cambios externos y muestras grandes de uso de entrada. Los hallazgos incluyen evidencia y una acción sugerida."
      },
      {
        "id": "security",
        "title": "Señales de seguridad",
        "desc": "Inspecciona comandos, solicitudes de URL y señales de riesgo basadas en patrones en traces GenAI. Son señales para revisión, no un veredicto de seguridad."
      }
    ]
  },
  "video": "Ver el recorrido de una sesión",
  "context": {
    "title": "Observaciones que el agente puede leer",
    "cards": [
      {
        "title": "Contexto para el siguiente turno",
        "desc": "Los hooks de Claude Code y Codex proporcionan contexto y avisos. La revisión de prompts puede usar un modelo configurado para sugerir cambios respaldados por fragmentos de conversación."
      },
      {
        "title": "Buscar entre sesiones",
        "desc": "Las herramientas MCP y las skills /tma1-peer y /tma1-search recuperan conversaciones anteriores, incluido el trabajo de otros agentes en el mismo proyecto."
      }
    ]
  },
  "how": {
    "title": "Ejecutar localmente",
    "label": "Pega esta instrucción en Claude Code o Codex",
    "manual": "Instalación manual",
    "opencode": "Configurar OpenCode →",
    "otel": "SDKs OpenTelemetry: envía OTLP/HTTP a http://localhost:14318/v1/otlp. Los traces GenAI usan las convenciones semánticas GenAI de OpenTelemetry.",
    "note": "OpenCode importa sesiones mediante un plugin o una exportación. La importación no instala hooks ni inyecta contexto. Los logs de Copilot CLI se descubren automáticamente."
  },
  "security": {
    "title": "Datos locales. Acceso explícito al modelo.",
    "desc": "Los traces y las conversaciones se guardan en ~/.tma1/data/ con GreptimeDB. Las revisiones opcionales con modelos envían los prompts seleccionados y fragmentos de conversación al proveedor configurado.",
    "link": "Documentación de integración →"
  },
  "footer": {
    "tagline": "TMA1 · Observabilidad local para agentes. Apache-2.0."
  },
  "ui": {
    "copy": "Copiar",
    "copied": "Copiado",
    "failed": "No se pudo copiar; selecciona el texto para copiarlo."
  }
};

export const locales = { en, zh, es } as const;
