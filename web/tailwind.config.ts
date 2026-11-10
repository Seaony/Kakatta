import type { Config } from 'tailwindcss';

const colors = Object.fromEntries(
  [
    'surface', 'raised', 'inset', 'sidebar', 'sidebar-selection', 'control',
    'control-hover', 'control-pressed', 'button-fill', 'segment-selected',
    'text-primary', 'text-body', 'text-label', 'text-secondary', 'accent',
    'accent-text', 'accent-fill', 'on-accent', 'border', 'positive',
    'positive-fill', 'warning', 'warning-fill', 'danger', 'danger-fill',
    'info', 'info-fill', 'focus',
  ].map((name) => [name, `rgb(var(--color-${name}) / <alpha-value>)`]),
);

export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    colors: {
      transparent: 'transparent', current: 'currentColor', ...colors,
      border: 'rgb(var(--color-border) / calc(var(--opacity-border) * <alpha-value>))',
      overlay: 'rgb(var(--color-overlay) / calc(var(--opacity-overlay) * <alpha-value>))',
    },
    spacing: {
      0: '0px', px: '1px',
      ...Object.fromEntries(
        ['xs', 'sm', 'md', 'lg', 'group', 'xl', 'xxl'].map((name) => [name, `var(--space-${name})`]),
      ),
    },
    borderRadius: Object.fromEntries(
      ['minimum', 'thumb', 'control', 'banner', 'card', 'panel'].map((name) => [name, `var(--radius-${name})`]),
    ),
    fontFamily: {
      sans: ['var(--font-family-ui)'],
      mono: ['var(--font-family-mono)'],
    },
    fontSize: Object.fromEntries(
      ['caption', 'body', 'input', 'section', 'page'].map((name) => [name, `var(--font-${name})`]),
    ),
    extend: {
      minHeight: { control: 'var(--control-min-height)' },
      minWidth: { control: 'var(--control-min-height)' },
      boxShadow: { overlay: 'var(--shadow-overlay)' },
      transitionDuration: { hover: 'var(--motion-hover)', state: 'var(--motion-state)' },
    },
  },
  plugins: [],
} satisfies Config;
