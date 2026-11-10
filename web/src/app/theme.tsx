import { createContext, useContext, useLayoutEffect, useState, useSyncExternalStore } from 'react';
import type { ReactNode } from 'react';

export type ThemeMode = 'light' | 'dark' | 'system';
export type ResolvedTheme = Exclude<ThemeMode, 'system'>;

type ThemeValue = {
  theme: ThemeMode;
  resolvedTheme: ResolvedTheme;
  setTheme(theme: ThemeMode): void;
};

const ThemeContext = createContext<ThemeValue | null>(null);
const mediaQuery = '(prefers-color-scheme: dark)';

function readSystemTheme(): ResolvedTheme {
  return typeof window !== 'undefined' && window.matchMedia(mediaQuery).matches ? 'dark' : 'light';
}

function subscribeSystemTheme(changed: () => void) {
  if (typeof window === 'undefined') return () => {};
  const media = window.matchMedia(mediaQuery);
  media.addEventListener('change', changed);
  return () => media.removeEventListener('change', changed);
}

export function ThemeProvider({ initialTheme = 'system', children }: { initialTheme?: ThemeMode; children: ReactNode }) {
  const [theme, setTheme] = useState(initialTheme);
  const systemTheme = useSyncExternalStore(subscribeSystemTheme, readSystemTheme, () => 'light' as const);
  const resolvedTheme = theme === 'system' ? systemTheme : theme;

  useLayoutEffect(() => {
    const root = document.documentElement;
    const previous = root.getAttribute('data-theme');
    const previousScheme = root.style.colorScheme;
    root.setAttribute('data-theme', resolvedTheme);
    root.style.colorScheme = resolvedTheme;
    return () => {
      if (previous === null) root.removeAttribute('data-theme');
      else root.setAttribute('data-theme', previous);
      root.style.colorScheme = previousScheme;
    };
  }, [resolvedTheme]);

  return <ThemeContext.Provider value={{ theme, resolvedTheme, setTheme }}>{children}</ThemeContext.Provider>;
}

export function useTheme(): ThemeValue {
  const value = useContext(ThemeContext);
  if (!value) throw new Error('主题组件需要放在 ThemeProvider 里');
  return value;
}
