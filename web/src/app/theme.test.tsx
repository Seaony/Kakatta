import { expect, test } from 'bun:test';
import { act, fireEvent, render, screen } from '@testing-library/react';
import { ThemeProvider, useTheme } from './theme';

function Probe() {
  const { theme, resolvedTheme, setTheme } = useTheme();
  return <>
    <p role="status">{theme}/{resolvedTheme}</p>
    <button onClick={() => setTheme('light')}>浅色</button>
    <button onClick={() => setTheme('system')}>跟随系统</button>
  </>;
}

function mockSystemTheme(initialDark: boolean) {
  const original = window.matchMedia;
  const listeners = new Set<() => void>();
  let dark = initialDark;
  const media = {
    get matches() { return dark; },
    media: '(prefers-color-scheme: dark)',
    addEventListener(_name: string, changed: () => void) { listeners.add(changed); },
    removeEventListener(_name: string, changed: () => void) { listeners.delete(changed); },
  } as unknown as MediaQueryList;
  window.matchMedia = () => media;
  return {
    listeners,
    change(value: boolean) { dark = value; act(() => { for (const changed of listeners) changed(); }); },
    restore() { window.matchMedia = original; },
  };
}

test('跟随系统主题并在卸载时移除监听、归还根节点状态', () => {
  const system = mockSystemTheme(true);
  const rendered = render(<ThemeProvider initialTheme="system"><Probe /></ThemeProvider>);
  try {
    expect(screen.getByRole('status').textContent).toBe('system/dark');
    expect(document.documentElement.dataset.theme).toBe('dark');
    expect(document.documentElement.style.colorScheme).toBe('dark');
    system.change(false);
    expect(screen.getByRole('status').textContent).toBe('system/light');
    expect(document.documentElement.dataset.theme).toBe('light');
    expect(system.listeners.size).toBe(1);
    rendered.unmount();
    expect(system.listeners.size).toBe(0);
    expect(document.documentElement.hasAttribute('data-theme')).toBe(false);
  } finally { rendered.unmount(); system.restore(); }
});

test('显式主题优先，重新跟随系统时采用当前系统值', () => {
  const system = mockSystemTheme(false);
  const rendered = render(<ThemeProvider initialTheme="light"><Probe /></ThemeProvider>);
  try {
    system.change(true);
    expect(screen.getByRole('status').textContent).toBe('light/light');
    expect(document.documentElement.dataset.theme).toBe('light');
    fireEvent.click(screen.getByRole('button', { name: '跟随系统' }));
    expect(screen.getByRole('status').textContent).toBe('system/dark');
    fireEvent.click(screen.getByRole('button', { name: '浅色' }));
    expect(screen.getByRole('status').textContent).toBe('light/light');
  } finally { rendered.unmount(); system.restore(); }
});
