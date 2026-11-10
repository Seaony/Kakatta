import { expect, test } from 'bun:test';
import { createMemoryHistory } from '@tanstack/react-router';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createApiClient } from '../api/client';
import { ApiError } from '../api/errors';
import { createI18n, I18nProvider, selectLocale } from '../i18n';
import { RootError } from '../routes/__root';
import { AppProviders } from './providers';
import { createAppQueryClient } from './query-client';
import { createAppRouter } from './router';

test('根应用共用 QueryClient，基础首屏不编造业务请求', async () => {
  const queryClient = createAppQueryClient();
  let calls = 0;
  const transport = async () => { calls++; return Response.json({}); };
  const router = createAppRouter({ queryClient, api: createApiClient({ fetch: transport }) }, createMemoryHistory({ initialEntries: ['/'] }));
  await router.load();
  const page = render(<AppProviders router={router} queryClient={queryClient} initialLocale="zh-CN" initialTheme="light" />);
  try {
    await waitFor(() => expect(screen.getByRole('heading', { level: 1, name: 'Kakatta' })).toBeTruthy());
    expect(screen.getByText('网页基础已就绪')).toBeTruthy();
    expect(screen.getByRole('link', { name: '跳到正文' }).getAttribute('href')).toBe('#main');
    expect(router.options.context.queryClient).toBe(queryClient);
    expect(calls).toBe(0);
  } finally { page.unmount(); queryClient.clear(); }
});

test('未知路径使用双语可访问返回入口，不变成空白页', async () => {
  const queryClient = createAppQueryClient();
  const router = createAppRouter({ queryClient, api: createApiClient() }, createMemoryHistory({ initialEntries: ['/missing'] }));
  await router.load();
  const page = render(<AppProviders router={router} queryClient={queryClient} initialLocale="en" initialTheme="dark" />);
  try {
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Page not found' })).toBeTruthy());
    expect(screen.getByRole('link', { name: 'Back to home' }).getAttribute('href')).toBe('/');
    expect(document.documentElement.lang).toBe('en');
  } finally { page.unmount(); queryClient.clear(); }
});

test('系统英文作为入口初值，显式中文选择仍然保留', async () => {
  const descriptor = Object.getOwnPropertyDescriptor(navigator, 'language');
  Object.defineProperty(navigator, 'language', { configurable: true, value: 'en-US' });
  try {
    const cases = [
      { locale: selectLocale('system'), expected: 'en', title: 'Web foundation is ready' },
      { locale: 'zh-CN', expected: 'zh-CN', title: '网页基础已就绪' },
    ] as const;
    for (const { locale, expected, title } of cases) {
      const queryClient = createAppQueryClient();
      const router = createAppRouter({ queryClient, api: createApiClient() }, createMemoryHistory({ initialEntries: ['/'] }));
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

test('根错误边界不显示未知原因代码，恢复按钮调用 reset', async () => {
  let retries = 0;
  const failure = new ApiError(409, { code: 'private.unknown', args: {} });
  render(<I18nProvider instance={createI18n('zh-CN')}><RootError error={failure} reset={() => { retries++; }} /></I18nProvider>);
  expect(screen.getByRole('alert').textContent).toContain('请求未完成，请重试。');
  expect(screen.queryByText('private.unknown')).toBeNull();
  await userEvent.click(screen.getByRole('button', { name: '重试' }));
  expect(retries).toBe(1);
});
