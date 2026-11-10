import { expect, test } from 'bun:test';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { ApiProvider, apiData, useApiError, useMessageText, useTimeZone } from './hooks';
import { createApiClient } from './client';
import { ApiError } from './errors';
import { createI18n, I18nProvider } from '../i18n';
import { createAppQueryClient } from '../app/query-client';

test('成功对象保留空值，缺少已声明响应才报格式错误', () => {
  expect(apiData({ data: null })).toBeNull();
  expect(apiData({ data: false })).toBe(false);
  expect(apiData({ data: 0 })).toBe(0);
  expect(() => apiData({})).toThrow(ApiError);
});

test('共享原因与HTTP错误统一安全翻译，不显示未知代码或原始正文', () => {
  const wrapper = ({ children }: { children: ReactNode }) => <I18nProvider instance={createI18n('en')}>{children}</I18nProvider>;
  const hook = renderHook(() => ({ error: useApiError(), message: useMessageText() }), { wrapper });
  expect(hook.result.current.message({ code: 'private.raw_payload', args: { secret: 'do-not-show' } })).toBe('The request did not complete. Please try again.');
  expect(hook.result.current.error(new ApiError(409, { code: 'private.raw_payload', args: {} }))).not.toContain('private.');
  expect(hook.result.current.error(new Error('private response'))).not.toContain('private response');
  hook.unmount();
});

test('时区只来自共享设置，加载中未知；卸载会取消读取', async () => {
  const queryClient = createAppQueryClient();
  let request: Request | undefined;
  let resolve: ((response: Response) => void) | undefined;
  const api = createApiClient({ fetch: (current) => {
    request = current;
    return new Promise<Response>((done) => { resolve = done; });
  } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={queryClient}><ApiProvider api={api}>{children}</ApiProvider></QueryClientProvider>;
  const hook = renderHook(() => ({ one: useTimeZone(), two: useTimeZone() }), { wrapper });
  expect(hook.result.current.one).toBeUndefined();
  await waitFor(() => expect(request).toBeDefined());
  resolve?.(Response.json({ values: { 'general.timezone': 'Asia/Tokyo' }, sources: {}, warnings: [] }));
  await waitFor(() => expect(hook.result.current).toEqual({ one: 'Asia/Tokyo', two: 'Asia/Tokyo' }));
  hook.unmount();
  queryClient.clear();

  const pendingClient = createAppQueryClient();
  let pending: Request | undefined;
  const pendingAPI = createApiClient({ fetch: (current) => { pending = current; return new Promise<Response>(() => {}); } });
  const pendingWrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={pendingClient}><ApiProvider api={pendingAPI}>{children}</ApiProvider></QueryClientProvider>;
  const canceled = renderHook(() => useTimeZone(), { wrapper: pendingWrapper });
  await waitFor(() => expect(pending).toBeDefined());
  canceled.unmount();
  expect(pending?.signal.aborted).toBe(true);
  pendingClient.clear();
});
