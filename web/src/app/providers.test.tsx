import { expect, test } from 'bun:test';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createApiClient } from '../api/client';
import { ApiError } from '../api/errors';
import type { Schema } from '../api/types';
import { createI18n, I18nProvider, selectLocale } from '../i18n';
import { RootError } from '../routes/__root';
import { AppProviders } from './providers';
import { createAppQueryClient } from './query-client';
import { createAppRouter } from './router';

const identity: Schema<'Me'> = { username: 'owner', auth_kind: 'web_session', token_id: 1, language: 'en', appearance: 'system', totp_enabled: false };

function localAPI(authenticated: boolean, requested: string[] = []) {
  return createApiClient({ fetch: async (request) => {
    const path = new URL(request.url).pathname;
    requested.push(path);
    if (path === '/api/v1/setup') return Response.json({ initialized: true });
    if (path === '/api/v1/me') return authenticated ? Response.json(identity) : Response.json({ error: { code: 'auth.unauthenticated', args: {} }, fields: [], request_id: 'local-auth' }, { status: 401 });
    if (path === '/api/v1/settings') return Response.json({ values: { 'general.onboarding_done': true }, sources: {}, warnings: [] });
    throw new Error(`测试没有提供这个本机接口：${path}`);
  } });
}

function disableEventStream() {
  const previous = Object.getOwnPropertyDescriptor(globalThis, 'EventSource');
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: undefined });
  return () => { if (previous) Object.defineProperty(globalThis, 'EventSource', previous); else Reflect.deleteProperty(globalThis, 'EventSource'); };
}

test('根应用先核初始化与身份，未登录不读取受保护业务数据', async () => {
  const queryClient = createAppQueryClient();
  const requests: string[] = [];
  const router = createAppRouter({ queryClient, api: localAPI(false, requests) }, createMemoryHistory({ initialEntries: ['/'] }));
  await router.load();
  const page = render(<AppProviders router={router} queryClient={queryClient} initialLocale="zh-CN" initialTheme="light" />);
  try {
    await waitFor(() => expect(screen.getByRole('heading', { level: 1, name: '登录' })).toBeTruthy());
    expect(screen.getByRole('link', { name: '跳到正文' }).getAttribute('href')).toBe('#main');
    expect(router.options.context.queryClient).toBe(queryClient);
    expect(requests).toContain('/api/v1/setup');
    expect(requests).toContain('/api/v1/me');
    expect(requests.every((path) => path === '/api/v1/setup' || path === '/api/v1/me')).toBe(true);
  } finally { page.unmount(); queryClient.clear(); }
});

test('未知路径使用双语可访问返回入口，不被业务首页吞掉', async () => {
  const restore = disableEventStream();
  const queryClient = createAppQueryClient();
  const router = createAppRouter({ queryClient, api: localAPI(true) }, createMemoryHistory({ initialEntries: ['/missing'] }));
  await router.load();
  const page = render(<AppProviders router={router} queryClient={queryClient} initialLocale="en" initialTheme="dark" />);
  try {
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Page not found' })).toBeTruthy());
    expect(screen.getByRole('link', { name: 'Back to home' }).getAttribute('href')).toBe('/');
    expect(document.documentElement.lang).toBe('en');
  } finally { page.unmount(); queryClient.clear(); restore(); }
});

test.each([
  { language: 'en-US', expected: 'en', title: 'Sign in' },
  { language: 'ja-JP', expected: 'ja', title: 'ログイン' },
  { language: 'fr-FR', expected: 'en', title: 'Sign in' },
])('系统语言 $language 作为入口初值，显式中文调用者选择仍然保留', async ({ language, expected, title }) => {
  const descriptor = Object.getOwnPropertyDescriptor(navigator, 'language');
  Object.defineProperty(navigator, 'language', { configurable: true, value: language });
  try {
    const cases = [
      { locale: selectLocale('system'), expected, title },
      { locale: 'zh-CN', expected: 'zh-CN', title: '登录' },
    ] as const;
    for (const { locale, expected, title } of cases) {
      const queryClient = createAppQueryClient();
      const router = createAppRouter({ queryClient, api: localAPI(false) }, createMemoryHistory({ initialEntries: ['/login'] }));
      await router.load();
      const page = render(<AppProviders router={router} queryClient={queryClient} initialLocale={locale} initialTheme="light" />);
      try {
        await waitFor(() => expect(screen.getByRole('heading', { name: title })).toBeTruthy());
        expect(document.documentElement.lang).toBe(expected);
      } finally { page.unmount(); queryClient.clear(); }
    }
  } finally {
    if (descriptor) Object.defineProperty(navigator, 'language', descriptor);
    else Reflect.deleteProperty(navigator, 'language');
  }
});

test('根错误边界不显示未知原因代码，恢复按钮调用reset', async () => {
  let retries = 0;
  const failure = new ApiError(409, { code: 'private.unknown', args: {} });
  render(<I18nProvider instance={createI18n('zh-CN')}><RootError error={failure} reset={() => { retries++; }} /></I18nProvider>);
  expect(screen.getByRole('alert').textContent).toContain('请求未完成，请重试。');
  expect(screen.queryByText('private.unknown')).toBeNull();
  await userEvent.click(screen.getByRole('button', { name: '重试' }));
  expect(retries).toBe(1);
});
