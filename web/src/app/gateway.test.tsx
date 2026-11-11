import { expect, test } from 'bun:test';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRouteWithContext, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { useState } from 'react';
import { createApiClient, type ApiFetch } from '../api/client';
import { ApiProvider } from '../api/hooks';
import { apiKey } from '../api/keys';
import type { Schema } from '../api/types';
import { createI18n, I18nProvider } from '../i18n';
import { Gateway } from './gateway';
import { ThemeProvider } from './theme';
import { createAppQueryClient } from './query-client';
import type { RouterContext } from './router';
import { UnsavedChangesProvider, useUnsavedChanges } from './unsaved-changes';
import { BackupPage } from '../features/admin/backup';

const user: Schema<'Me'> = { username: 'owner', auth_kind: 'web_session', token_id: 1, language: 'zh-CN', appearance: 'light', totp_enabled: false };

function Draft() {
  const [value, setValue] = useState('');
  useUnsavedChanges(value.length > 0);
  return <input aria-label="本地草稿" value={value} onChange={(event) => setValue(event.currentTarget.value)} />;
}

async function mountGateway(fetch: ApiFetch, backup = false) {
  const eventSource = Object.getOwnPropertyDescriptor(globalThis, 'EventSource');
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: undefined });
  const queryClient = createAppQueryClient();
  const api = createApiClient({ fetch });
  const root = createRootRouteWithContext<RouterContext>()({ component: () => <UnsavedChangesProvider><Gateway><Draft />{backup && <BackupPage />}</Gateway></UnsavedChangesProvider> });
  const route = createRoute({ getParentRoute: () => root, path: '/draft' });
  const login = createRoute({ getParentRoute: () => root, path: '/login' });
  const setup = createRoute({ getParentRoute: () => root, path: '/setup' });
  const router = createRouter({ routeTree: root.addChildren([route, login, setup]), context: { queryClient, api }, history: createMemoryHistory({ initialEntries: ['/draft'] }) });
  await router.load();
  const view = render(<I18nProvider instance={createI18n('zh-CN')}><ThemeProvider initialTheme="light"><QueryClientProvider client={queryClient}><ApiProvider api={api}><RouterProvider router={router} /></ApiProvider></QueryClientProvider></ThemeProvider></I18nProvider>);
  return { api, queryClient, router, async dispose() {
    await act(async () => { view.unmount(); await queryClient.cancelQueries(); queryClient.clear(); });
    if (eventSource) Object.defineProperty(globalThis, 'EventSource', eventSource); else Reflect.deleteProperty(globalThis, 'EventSource');
  } };
}

test('登录后的日语偏好接入网页和文档语言，不被旧中文身份覆盖', async () => {
  const gateway = await mountGateway(async (request) => {
    const path = new URL(request.url).pathname;
    if (path === '/api/v1/setup') return Response.json({ initialized: true, token_expires_at: null });
    if (path === '/api/v1/me') return Response.json(user);
    if (path === '/api/v1/settings') return Response.json({ values: { 'general.language': 'ja' }, sources: {}, warnings: [] });
    throw new Error(`测试未声明路径：${path}`);
  });
  try {
    await screen.findByRole('link', { name: 'タスク' });
    expect(document.documentElement.lang).toBe('ja');
    expect(screen.getByRole('textbox', { name: '本地草稿' })).toBeTruthy();
  } finally { await gateway.dispose(); }
});

test.each([
  { label: '初始化', marker: 'true', initialized: false, authenticated: false },
  { label: '登录前', marker: 'true', initialized: true, authenticated: false },
  { label: '登录后', marker: 'true', initialized: true, authenticated: true },
  { label: '本地或已加密', marker: 'false', initialized: true, authenticated: true },
])('公网明文提示由服务端标记决定：$label', async ({ marker, initialized, authenticated }) => {
  const dataset = document.documentElement.dataset;
  const previous = dataset.kakattaPublicHttp;
  dataset.kakattaPublicHttp = marker;
  const gateway = await mountGateway(async (request) => {
    const path = new URL(request.url).pathname;
    if (path === '/api/v1/setup') return Response.json({ initialized, token_expires_at: null });
    if (path === '/api/v1/settings') return Response.json({ values: {}, sources: {}, warnings: [] });
    if (path === '/api/v1/me' && authenticated) return Response.json(user);
    if (path === '/api/v1/me' || path === '/api/v1/tasks') return Response.json({ error: { code: 'auth.unauthenticated', args: {} }, fields: [], request_id: 'login' }, { status: 401 });
    throw new Error(`测试未声明路径：${path}`);
  });
  try {
    if (!initialized) await waitFor(() => expect(gateway.router.state.location.pathname).toBe('/setup'));
    if (initialized && !authenticated) await screen.findByRole('heading', { name: '登录' });
    if (authenticated) await screen.findByRole('textbox', { name: '本地草稿' });
    if (marker === 'true') {
      const alert = await screen.findByRole('alert');
      expect(alert.textContent).toContain('当前连接未加密');
      expect(alert.textContent).toContain('请切换到 HTTPS');
      if (authenticated) {
        await act(async () => { await gateway.api.GET('/api/v1/tasks').catch(() => {}); });
        const dialog = await screen.findByRole('dialog', { name: '登录已失效' });
        expect(within(dialog).getByRole('alert').textContent).toContain('当前连接未加密');
      }
    } else {
      expect(screen.queryByRole('alert')).toBeNull();
    }
  } finally {
    await gateway.dispose();
    if (previous === undefined) delete dataset.kakattaPublicHttp; else dataset.kakattaPublicHttp = previous;
  }
});

test('登录失效保留原页非敏感草稿，明确重新登录后恢复同一组件', async () => {
  let expired = false, logins = 0;
  const gateway = await mountGateway(async (request) => {
    const path = new URL(request.url).pathname;
    if (path === '/api/v1/setup') return Response.json({ initialized: true, token_expires_at: null });
    if (path === '/api/v1/settings') return Response.json({ values: { 'general.timezone': 'Asia/Tokyo' }, sources: {}, warnings: [] });
    if (path === '/api/v1/auth/login') { logins++; expired = false; return Response.json({ status: 'authenticated', user }); }
    if (path === '/api/v1/tasks' || path === '/api/v1/me' && expired) return Response.json({ error: { code: 'auth.unauthenticated', args: {} }, fields: [], request_id: 'expired' }, { status: 401 });
    if (path === '/api/v1/me') return Response.json(user);
    throw new Error(`测试未声明路径：${path}`);
  });
  try {
    const input = await screen.findByRole<HTMLInputElement>('textbox', { name: '本地草稿' });
    await userEvent.type(input, '保留我的筛选草稿');
    expired = true;
    await act(async () => { await gateway.api.GET('/api/v1/tasks').catch(() => {}); });
    const dialog = await screen.findByRole('dialog', { name: '登录已失效' });
    expect(input.value).toBe('保留我的筛选草稿');
    expect(input.closest('[hidden]')).not.toBeNull();
    expect(gateway.router.state.location.pathname).toBe('/draft');
    await userEvent.type(within(dialog).getByRole('textbox', { name: '用户名' }), 'owner');
    await userEvent.type(within(dialog).getByLabelText('登录密码'), '本机假密码');
    await userEvent.click(within(dialog).getByRole('button', { name: '登录' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await waitFor(() => expect(gateway.queryClient.isFetching()).toBe(0));
    expect(screen.getByRole('textbox', { name: '本地草稿' })).toBe(input);
    expect(input.value).toBe('保留我的筛选草稿');
    expect(logins).toBe(1);
    expect(gateway.router.state.location.pathname).toBe('/draft');
  } finally { await gateway.dispose(); }
});

test('已有身份刷新失败保留草稿与旧内容，不伪造成退出登录', async () => {
  let unavailable = false;
  const gateway = await mountGateway(async (request) => {
    const path = new URL(request.url).pathname;
    if (path === '/api/v1/setup') return Response.json({ initialized: true, token_expires_at: null });
    if (path === '/api/v1/settings') return Response.json({ values: {}, sources: {}, warnings: [] });
    if (path === '/api/v1/me') return unavailable ? Response.json({ error: { code: 'http.unavailable', args: {} }, fields: [], request_id: 'offline' }, { status: 503 }) : Response.json(user);
    throw new Error(`测试未声明路径：${path}`);
  });
  try {
    const input = await screen.findByRole<HTMLInputElement>('textbox', { name: '本地草稿' });
    await userEvent.type(input, '还没保存');
    unavailable = true;
    await act(async () => { await gateway.queryClient.resetQueries({ queryKey: apiKey('me') }); });
    await screen.findByRole('alert');
    expect(screen.getByRole('textbox', { name: '本地草稿' })).toBe(input);
    expect(input.value).toBe('还没保存');
    expect(screen.queryByRole('dialog')).toBeNull();
  } finally { await gateway.dispose(); }
});

test.each([false, true])('退出先确认未保存内容，选择继续退出为%s', async (confirmed) => {
  let logouts = 0, signedOut = false;
  const gateway = await mountGateway(async (request) => {
    const path = new URL(request.url).pathname;
    if (path === '/api/v1/setup') return Response.json({ initialized: true, token_expires_at: null });
    if (path === '/api/v1/settings') return Response.json({ values: {}, sources: {}, warnings: [] });
    if (path === '/api/v1/me') return signedOut ? Response.json({ error: { code: 'auth.unauthenticated', args: {} }, fields: [], request_id: 'signed-out' }, { status: 401 }) : Response.json(user);
    if (path === '/api/v1/auth/logout') { logouts++; signedOut = true; return new Response(null, { status: 204 }); }
    throw new Error(`测试未声明路径：${path}`);
  }, true);
  try {
    const input = await screen.findByRole<HTMLInputElement>('textbox', { name: '本地草稿' });
    await userEvent.type(input, '尚未保存的草稿');
    await userEvent.type(screen.getByLabelText('备份密码'), 'local-private-password');
    const logout = screen.getAllByRole('button', { name: '退出 Kakatta' })[0];
    if (!logout) throw new Error('缺少退出按钮');
    await userEvent.click(logout);
    const dialog = await screen.findByRole('alertdialog', { name: '放弃未保存的修改？' });
    expect(logouts).toBe(0);
    expect(screen.getAllByRole('alertdialog')).toHaveLength(1);
    await userEvent.click(within(dialog).getByRole('button', { name: confirmed ? '放弃修改并离开' : '取消' }));
    if (!confirmed) {
      expect(logouts).toBe(0);
      expect(input.value).toBe('尚未保存的草稿');
      expect(screen.getByLabelText<HTMLInputElement>('备份密码').value).toBe('local-private-password');
      expect(gateway.router.state.location.pathname).toBe('/draft');
    } else {
      await waitFor(() => expect(gateway.router.state.location.pathname).toBe('/login'));
      await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());
      expect(logouts).toBe(1);
      expect(gateway.queryClient.getQueryData(apiKey('me'))).toBeUndefined();
      expect(screen.queryByDisplayValue('local-private-password')).toBeNull();
      expect(screen.queryByDisplayValue('尚未保存的草稿')).toBeNull();
    }
  } finally { await gateway.dispose(); }
});
