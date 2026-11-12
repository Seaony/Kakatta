import { expect, test } from 'bun:test';
import { act, render, screen, waitFor } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from '@tanstack/react-router';
import { createApiClient } from '../api/client';
import { ApiProvider } from '../api/hooks';
import { apiKey } from '../api/keys';
import { AuthenticationContext, type AuthenticationState } from '../app/auth-state';
import { createAppQueryClient } from '../app/query-client';
import { createI18n, I18nProvider } from '../i18n';
import { Route } from './index';

const Start = Route.options.component!;

test.each([
  { label: '未登录', context: false, done: true, destination: '/overview', cached: false },
  { label: '未登录', context: false, done: false, destination: '/onboarding', cached: false },
  { label: '没有认证上下文', context: null, done: true, destination: '/overview', cached: false },
  { label: '没有认证上下文', context: null, done: false, destination: '/onboarding', cached: false },
  { label: '未登录', context: false, done: true, destination: '/overview', cached: true },
  { label: '未登录', context: false, done: false, destination: '/onboarding', cached: true },
  { label: '没有认证上下文', context: null, done: true, destination: '/overview', cached: true },
  { label: '没有认证上下文', context: null, done: false, destination: '/onboarding', cached: true },
])('$label / 缓存=$cached / 登录后引导状态=$done / 目标=$destination', async ({ context, done, destination, cached }) => {
  const requests: string[] = [];
  const api = createApiClient({ fetch: async (request) => {
    const path = new URL(request.url).pathname;
    requests.push(request.method + ' ' + path);
    if (path === '/api/v1/settings') return Response.json({ values: { 'general.onboarding_done': done }, sources: {}, warnings: [] });
    throw new Error(`测试没有提供这个本机接口：${path}`);
  } });
  const queryClient = createAppQueryClient();
  if (cached) queryClient.setQueryData(apiKey('settings'), { values: { 'general.onboarding_done': done }, sources: {}, warnings: [] });
  const i18n = createI18n('zh-CN');
  const root = createRootRoute({ component: Outlet });
  const index = createRoute({ getParentRoute: () => root, path: '/', component: Start });
  const overview = createRoute({ getParentRoute: () => root, path: '/overview', component: () => <h1>测试总览</h1> });
  const onboarding = createRoute({ getParentRoute: () => root, path: '/onboarding', component: () => <h1>测试引导</h1> });
  const router = createRouter({ routeTree: root.addChildren([index, overview, onboarding]), history: createMemoryHistory({ initialEntries: ['/'] }) });
  await router.load();
  function Harness({ authenticated }: { authenticated: boolean | null }) {
    const state: AuthenticationState | null = authenticated === null ? null : { authenticated, user: undefined, complete() {}, refresh() {} };
    return <AuthenticationContext.Provider value={state}><I18nProvider instance={i18n}><QueryClientProvider client={queryClient}><ApiProvider api={api}><RouterProvider router={router} /></ApiProvider></QueryClientProvider></I18nProvider></AuthenticationContext.Provider>;
  }
  const view = render(<Harness authenticated={context} />);
  try {
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(requests).toEqual([]);
    expect(router.state.location.pathname).toBe('/');
    expect(screen.getByRole('status')).toBeTruthy();
    view.rerender(<Harness authenticated />);
    await screen.findByRole('heading', { name: done ? '测试总览' : '测试引导' });
    await waitFor(() => expect(router.state.location.pathname).toBe(destination));
    expect(requests).toEqual(['GET /api/v1/settings']);
  } finally {
    view.unmount();
    await queryClient.cancelQueries();
    queryClient.clear();
  }
});
