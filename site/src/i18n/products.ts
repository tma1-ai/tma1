export type ProductId = 'tma1' | 'openfuse' | 'dsh-otel';
export type Locale = 'en' | 'zh' | 'es';

export interface Product {
  id: ProductId;
  /** Tab and footer label. Never translated. */
  name: string;
  /** URL segment under the locale root. Empty for the default product. */
  segment: string;
  repo: string;
  /** One line shown as the tab's title attribute and in the footer product row. */
  blurb: Record<Locale, string>;
}

export const products: Product[] = [
  {
    id: 'tma1',
    name: 'TMA1',
    segment: '',
    repo: 'https://github.com/tma1-ai/tma1',
    blurb: {
      en: 'Local-first agent observability your agent reads back',
      zh: '本地优先的 agent 可观测性，并把结果回灌给 agent',
      es: 'Observabilidad local para agentes, que el agente vuelve a leer',
    },
  },
  {
    id: 'openfuse',
    name: 'Openfuse',
    segment: 'openfuse',
    repo: 'https://github.com/tma1-ai/openfuse',
    blurb: {
      en: 'LLM engineering on a real observability database',
      zh: '跑在真正的可观测数据库上的 LLM 工程平台',
      es: 'Ingeniería de LLM sobre una base de datos de observabilidad real',
    },
  },
  {
    id: 'dsh-otel',
    name: 'DSH OTel',
    segment: 'dsh-otel',
    repo: 'https://github.com/tma1-ai/dsh-otel',
    blurb: {
      en: 'DeepSeek Harness telemetry as plain OpenTelemetry',
      zh: '把 DeepSeek Harness 的遥测变成标准 OpenTelemetry',
      es: 'Telemetría de DeepSeek Harness como OpenTelemetry',
    },
  },
];

export function productById(id: ProductId): Product {
  const p = products.find(x => x.id === id);
  if (!p) throw new Error(`unknown product: ${id}`);
  return p;
}

/** Site-root-relative URL for a product in a locale. Always trailing-slashed. */
export function productHref(id: ProductId, lang: Locale): string {
  const { segment } = productById(id);
  const localePrefix = lang === 'en' ? '' : `${lang}/`;
  return `/${localePrefix}${segment}${segment ? '/' : ''}`;
}
