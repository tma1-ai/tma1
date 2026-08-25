import type { Locale } from './products';

/** Strings owned by the shared header and footer, independent of which product page renders them. */
export interface Chrome {
  products_label: string;
  theme_label: string;
  theme_light: string;
  theme_dark: string;
  theme_system: string;
  copy: string;
  copied: string;
  more_projects: string;
}

export const chrome: Record<Locale, Chrome> = {
  en: {
    products_label: 'Projects',
    theme_label: 'Theme',
    theme_light: 'Light',
    theme_dark: 'Dark',
    theme_system: 'System',
    copy: 'Copy',
    copied: 'Copied!',
    more_projects: 'More from tma1-ai',
  },
  zh: {
    products_label: '项目',
    theme_label: '主题',
    theme_light: '浅色',
    theme_dark: '深色',
    theme_system: '跟随系统',
    copy: '复制',
    copied: '已复制！',
    more_projects: 'tma1-ai 的其他项目',
  },
  es: {
    products_label: 'Proyectos',
    theme_label: 'Tema',
    theme_light: 'Claro',
    theme_dark: 'Oscuro',
    theme_system: 'Sistema',
    copy: 'Copiar',
    copied: '¡Copiado!',
    more_projects: 'Más de tma1-ai',
  },
};
