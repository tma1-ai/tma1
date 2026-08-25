import type { Locale } from './products';

/** One line of a rendered code / terminal block. `note` renders dimmed after the text. */
export interface CodeLine {
  kind?: 'text' | 'comment' | 'blank';
  text?: string;
  note?: string;
}

export interface CodeBlock {
  /** Text shown in the terminal title bar. */
  title: string;
  lines: CodeLine[];
}

export interface Card {
  title: string;
  desc: string;
}

export type Media =
  | { kind: 'image'; src: string; w: number; h: number; alt: string; chrome: string }
  | { kind: 'code'; block: CodeBlock }
  | { kind: 'table'; head: string[]; rows: string[][] };

export interface FeatureRow {
  num: string;
  title: string;
  desc: string;
  media: Media;
}

interface SectionBase {
  /** Anchor id, when a nav link points here. */
  id?: string;
  kicker: string;
  title: string;
  desc: string;
}

export type Section =
  | (SectionBase & { kind: 'panel'; panel_title: string; panel_body: string; code?: CodeBlock; cards?: Card[] })
  | (SectionBase & { kind: 'cards'; cards: Card[] })
  | (SectionBase & { kind: 'table'; head: string[]; rows: string[][]; note?: string })
  | (SectionBase & { kind: 'faq'; items: Array<{ q: string; a: string }> });

export interface ProductCopy {
  lang: Locale;
  title: string;
  description: string;
  og: { image: string; alt: string };
  nav: Array<{ href: string; label: string }>;

  hero: {
    eyebrow: string;
    h1_1: string;
    h1_2: string;
    subtitle: string;
    badges: Array<{ label: string; href: string }>;
  };

  install: {
    label: string;
    cmd: string;
    note: string;
    more_summary: string;
    more: CodeBlock[];
  };

  /** Optional block under the install card — the one query or snippet that sells it. */
  hook?: { block: CodeBlock; caption: string };

  highlights: Card[];

  features: {
    id: string;
    kicker: string;
    title: string;
    desc: string;
    rows: FeatureRow[];
  };

  sections: Section[];

  footer: { tagline: string; links: Array<{ href: string; label: string }> };
}
