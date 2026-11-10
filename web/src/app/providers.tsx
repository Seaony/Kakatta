import { QueryClientProvider, type QueryClient } from '@tanstack/react-query';
import { RouterProvider } from '@tanstack/react-router';
import { createI18n, I18nProvider, type Locale } from '../i18n';
import type { AppRouter } from './router';
import { ThemeProvider, type ThemeMode } from './theme';
import { useState } from 'react';
import { ApiProvider } from '../api/hooks';

export type AppProvidersProps = {
  router: AppRouter;
  queryClient: QueryClient;
  initialLocale: Locale;
  initialTheme: ThemeMode;
};

export function AppProviders({ router, queryClient, initialLocale, initialTheme }: AppProvidersProps) {
  const [i18n] = useState(() => createI18n(initialLocale));
  return (
    <I18nProvider instance={i18n}>
      <ThemeProvider initialTheme={initialTheme}>
        <QueryClientProvider client={queryClient}>
          <ApiProvider api={router.options.context.api}>
            <RouterProvider router={router} />
          </ApiProvider>
        </QueryClientProvider>
      </ThemeProvider>
    </I18nProvider>
  );
}
