import { afterEach, expect, test } from 'bun:test';
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createApiClient } from '../../api/client';
import { ApiProvider } from '../../api/hooks';
import type { components } from '../../api/schema.gen';
import { createI18n, I18nProvider } from '../../i18n';
import { SystemPage } from './system';

const at = 1_795_000_000_000;
const healthy: components['schemas']['SystemStatus'] = { as_of: at, scheduler: { running: true, accepting: true, running_count: 0, queued_count: 0, next_wake_at: null }, browsers: { active: 0, limit: 1, sessions: [] }, configuration: [], quotas: [], resources: { disk_total_bytes: null, disk_free_bytes: null, memory_total_bytes: null, memory_used_bytes: null } };
const noRelease: components['schemas']['UpdateStatus'] = { current_version: 'dev', enabled: true, error: null, known_release: null, last_attempt_at: at, last_success_at: at, next_check_at: at + 86_400_000, stale: false, update_available: false };
const clients: QueryClient[] = [];
afterEach(() => { clients.splice(0).forEach((client) => client.clear()); });

async function systemPage(locale: 'zh-CN' | 'en' | 'ja', readUpdate: () => Response, readStatus: () => Response | Promise<Response> = () => Response.json(healthy)) {
  const calls: { path: string; method: string }[] = [];
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } });
  clients.push(client);
  const api = createApiClient({ fetch: async (request) => {
    const path = new URL(request.url).pathname;
    calls.push({ path, method: request.method });
    if (path === '/healthz') return Response.json({ status: 'ok' });
    if (path === '/api/v1/system/status') return readStatus();
    if (path === '/api/v1/system/update') return readUpdate();
    if (path === '/api/v1/settings') return Response.json({ values: { 'general.timezone': 'UTC' }, sources: {}, warnings: [] });
    if (path === '/api/v1/settings/definitions') return Response.json([]);
    if (path === '/api/v1/system/events') return Response.json({ items: [], next_cursor: null, total: 0 });
    throw new Error(`没有假响应：${path}`);
  } });
  const root = createRootRoute({ component: SystemPage });
  const router = createRouter({ routeTree: root, history: createMemoryHistory({ initialEntries: ['/'] }) });
  await router.load();
  render(<I18nProvider instance={createI18n(locale)}><QueryClientProvider client={client}><ApiProvider api={api}><RouterProvider router={router} /></ApiProvider></QueryClientProvider></I18nProvider>);
  return { calls, user: userEvent.setup() };
}

for (const [locale, warning, refresh, health, about] of [
  ['zh-CN', '检查更新失败，稍后会自动重试。', '刷新', '服务可用', '更新服务端'],
  ['en', 'Could not check for updates. The server will try again later.', 'Refresh', 'Service available', 'Update the server'],
  ['ja', 'アップデートを確認できませんでした。後ほど自動で再試行します。', '更新', 'サービス利用可能', 'サーバーを更新'],
] as const) {
  test(`${locale} 系统页把更新网络失败显示为警告，恢复后刷新清除`, async () => {
    let failed = true;
    const fixture = await systemPage(locale, () => Response.json(failed ? { ...noRelease, stale: true, update_available: null, error: { code: 'update.check_failed', args: {} } } : noRelease));
    const notice = await screen.findByText(warning);
    expect(notice.closest('[role="status"]')).toBeTruthy();
    expect(notice.closest('[role="alert"]')).toBeNull();
    expect(screen.getByText(health)).toBeTruthy();
    expect(screen.getByRole('link', { name: about }).getAttribute('href')).toBe('/settings/about');
    failed = false;
    await fixture.user.click(screen.getByRole('button', { name: refresh }));
    await waitFor(() => expect(screen.queryByText(warning)).toBeNull());
    expect(fixture.calls.filter((call) => call.path === '/api/v1/system/update')).toHaveLength(2);
    expect(fixture.calls.every((call) => call.method === 'GET')).toBe(true);
  });
}

test('更新状态读取失败继续显示错误，不能当作没有新版本', async () => {
  await systemPage('zh-CN', () => Response.json({ error: { code: 'service.storage_failure', args: {} }, fields: [], request_id: 'local-update-read' }, { status: 503 }));
  expect(await screen.findByText('local-update-read', { exact: false })).toBeTruthy();
  expect(screen.queryByText('暂无可用更新。')).toBeNull();
});

for (const reason of ['runtime.storage_full', 'runtime.storage_locked', 'runtime.storage_unavailable']) {
  test(`${reason} 停机和刷新均不读更新缓存，状态恢复后才读取`, async () => {
    let stopped = true;
    const fixture = await systemPage('zh-CN', () => Response.json(noRelease), () => stopped
      ? Response.json({ error: { code: reason, args: {} }, fields: [], request_id: 'storage-state' }, { status: 503 })
      : Response.json(healthy));
    await screen.findByText('storage-state', { exact: false });
    expect(fixture.calls.filter((call) => call.path === '/api/v1/system/update')).toHaveLength(0);
    const refresh = screen.getByRole<HTMLButtonElement>('button', { name: '刷新' });
    await fixture.user.click(refresh);
    await waitFor(() => expect(refresh.disabled).toBe(false));
    expect(fixture.calls.filter((call) => call.path === '/api/v1/system/status')).toHaveLength(2);
    expect(fixture.calls.filter((call) => call.path === '/api/v1/system/update')).toHaveLength(0);
    stopped = false;
    await fixture.user.click(refresh);
    await screen.findByText('运行中');
    await waitFor(() => expect(fixture.calls.filter((call) => call.path === '/api/v1/system/update')).toHaveLength(1));
  });
}

test('正常页刷新发现存储停机时，不继续刷新更新状态', async () => {
  let stopped = false;
  const fixture = await systemPage('zh-CN', () => Response.json(noRelease), () => stopped
    ? Response.json({ error: { code: 'runtime.storage_locked', args: {} }, fields: [], request_id: 'new-storage-state' }, { status: 503 })
    : Response.json(healthy));
  await screen.findByText('运行中');
  await waitFor(() => expect(fixture.calls.filter((call) => call.path === '/api/v1/system/update')).toHaveLength(1));
  stopped = true;
  await fixture.user.click(screen.getByRole('button', { name: '刷新' }));
  await screen.findByText('new-storage-state', { exact: false });
  expect(fixture.calls.filter((call) => call.path === '/api/v1/system/update')).toHaveLength(1);
});
