import { createContext, useCallback, useContext, type ReactNode } from 'react';
import { useQuery } from '@tanstack/react-query';
import type { ApiClient } from './client';
import { ApiError } from './errors';
import { apiKey } from './keys';
import { formatMessage, useTranslation, type Message } from '../i18n';

const ApiContext = createContext<ApiClient | null>(null);

export function ApiProvider({ api, children }: { api: ApiClient; children: ReactNode }) {
  return <ApiContext.Provider value={api}>{children}</ApiContext.Provider>;
}

export function useApi(): ApiClient {
  const api = useContext(ApiContext);
  if (!api) throw new Error('接口组件需要放在 ApiProvider 里');
  return api;
}

export function apiData<T>(result: { data?: T }): T {
  if (result.data === undefined) throw new ApiError(0, { code: 'web.invalid_response', args: {} });
  return result.data;
}

export function useMessageText(): (message: Message) => string {
  const { locale, t } = useTranslation();
  return useCallback((message: Message) => {
    try { return formatMessage(message, locale); } catch { return t('web.request_failed'); }
  }, [locale, t]);
}

export function useApiError(): (error: unknown) => string {
  const messageText = useMessageText();
  const { t } = useTranslation();
  return useCallback((error: unknown) => error instanceof ApiError ? messageText(error.reason) : t('web.request_failed'), [messageText, t]);
}

export function useTimeZone(): string | undefined {
  const api = useApi();
  const settings = useQuery({
    queryKey: apiKey('settings'),
    queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings', { signal })),
    staleTime: 30_000,
  });
  const value = settings.data?.values['general.timezone'];
  return typeof value === 'string' && value.length > 0 ? value : undefined;
}
