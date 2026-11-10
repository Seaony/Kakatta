import { expect, mock, test } from 'bun:test';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState, type Dispatch, type ReactNode, type SetStateAction } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Link, Outlet, RouterProvider } from '@tanstack/react-router';
import { ApiProvider } from '../../api/hooks';
import { createApiClient, type ApiFetch } from '../../api/client';
import { apiKey } from '../../api/keys';
import { AuthenticationContext } from '../../app/auth-state';
import { UnsavedChangesProvider } from '../../app/unsaved-changes';
import { createI18n, I18nProvider } from '../../i18n';
import type { components } from '../../api/schema.gen';
import { ChannelEditor } from './channel-editor';
import { ChannelSetup } from './channels';
import { NotificationsPage } from './rules';
import type { ChannelAdapter, ChannelRow } from './types';

const row: ChannelRow = { id: 1, kind: 'telegram', name: '原渠道', enabled: true, fields: { chat_id: 'local-fixture' }, hasSecret: { token: true }, testedAt: null, testOK: null, testError: null };
const snapshot: components['schemas']['SettingsSnapshot'] = { values: { 'notify.link_target': 'mobile', 'notify.max_per_run': 5, 'notify.system_channel_ids': [], 'general.timezone': 'UTC', 'general.public_url': '' }, sources: {}, warnings: [] };
const receipt: components['schemas']['ChannelTestResult'] = { channel_id: 1, delivery_id: 2, at: 1700000000000, ok: true, error: null };
function deferred<Value>() {
  let finish: ((value: Value) => void) | undefined;
  const promise = new Promise<Value>((resolve) => { finish = resolve; });
  return { promise, resolve(value: Value) { if (!finish) throw new Error('测试等待尚未建立'); finish(value); } };
}
function adapterFixture() {
  const list = mock(async (_signal: AbortSignal) => [row]);
  const adapter: ChannelAdapter = { list, get: async () => row, save: async () => row, setEnabled: async () => row, remove: async () => {}, test: async () => receipt, history: async () => [], deliveryItems: async () => [] };
  return { adapter, list };
}
const readFixture: ApiFetch = async (request) => {
  const path = new URL(request.url).pathname;
  if (path === '/api/v1/settings') return Response.json(snapshot);
  if (path === '/api/v1/settings/definitions' || path === '/api/v1/channels') return Response.json([]);
  throw new Error('测试禁止未声明请求');
};
function SessionControls({ children, expose }: { children: ReactNode; expose(setAuthenticated: Dispatch<SetStateAction<boolean>>): void }) {
  const [authenticated, setAuthenticated] = useState(true); expose(setAuthenticated);
  return <AuthenticationContext.Provider value={{ authenticated, user: undefined, complete: () => setAuthenticated(true), refresh: () => {} }}>
    <button onClick={() => setAuthenticated(false)}>测试登录失效</button>
    <button onClick={() => setAuthenticated(true)}>测试重新登录</button>
    <Link to="/overview">去目标页</Link>
    {children}
  </AuthenticationContext.Provider>;
}
async function mount(element: ReactNode, fetch: ApiFetch = readFixture) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } } });
  const api = createApiClient({ fetch });
  let updateAuthentication: Dispatch<SetStateAction<boolean>> | undefined;
  const root = createRootRoute({ component: () => <UnsavedChangesProvider><Outlet /></UnsavedChangesProvider> });
  const edit = createRoute({ getParentRoute: () => root, path: '/settings/notifications', component: () => <SessionControls expose={(update) => { updateAuthentication = update; }}>{element}</SessionControls> });
  const target = createRoute({ getParentRoute: () => root, path: '/overview', component: () => <h1>测试目标页</h1> });
  const router = createRouter({ routeTree: root.addChildren([edit, target]), history: createMemoryHistory({ initialEntries: ['/settings/notifications'] }) });
  await router.load();
  const view = render(<I18nProvider instance={createI18n('zh-CN')}><ApiProvider api={api}><QueryClientProvider client={queryClient}><RouterProvider router={router} /></QueryClientProvider></ApiProvider></I18nProvider>);
  return { ...view, router, queryClient, async setAuthenticated(value: boolean) { await act(async () => { if (!updateAuthentication) throw new Error('测试会话尚未挂载'); updateAuthentication(value); }); } };
}
async function relogin(view: { setAuthenticated(value: boolean): Promise<void> }) {
  await view.setAuthenticated(false); await view.setAuthenticated(true);
}

test('旧会话渠道保存即使忽略取消也不能回调，新会话显式保存正常', async () => {
  const f = adapterFixture(); const pending = deferred<ChannelRow>(); const signals: AbortSignal[] = []; const saved = mock((_channel: ChannelRow) => {});
  const save = mock(async (_id: number | null, _draft: unknown, signal: AbortSignal) => { signals.push(signal); return pending.promise; }); f.adapter.save = save;
  const view = await mount(<ChannelEditor channel={row} adapter={f.adapter} onSaved={saved} onCancel={() => {}} />);
  const user = userEvent.setup(); const name = await screen.findByRole<HTMLInputElement>('textbox', { name: '名称' });
  await user.clear(name); await user.type(name, '旧请求'); await user.click(screen.getByRole('button', { name: '保存渠道' }));
  await relogin(view); await user.clear(name); await user.type(name, '新会话草稿');
  await act(async () => pending.resolve({ ...row, name: '旧结果' }));
  expect(signals[0]?.aborted).toBe(true); expect(saved).not.toHaveBeenCalled(); expect(name.value).toBe('新会话草稿'); expect(save).toHaveBeenCalledTimes(1);
  save.mockImplementationOnce(async () => ({ ...row, name: '新会话草稿' }));
  await user.click(screen.getByRole('button', { name: '保存渠道' })); await waitFor(() => expect(saved).toHaveBeenCalledTimes(1)); expect(saved.mock.calls[0]?.[0].name).toBe('新会话草稿'); expect(save).toHaveBeenCalledTimes(2);
});

for (const action of ['test', 'toggle', 'remove'] as const) test(`会话失效取消渠道${action}，迟到成功不刷新新会话缓存也不重发`, async () => {
  const f = adapterFixture(); const pending = deferred<void>(); const signals: AbortSignal[] = []; let calls = 0;
  const wait = async (signal: AbortSignal) => { calls++; signals.push(signal); await pending.promise; };
  f.adapter.test = async (_id, signal) => { await wait(signal); return receipt; };
  f.adapter.setEnabled = async (_id, _enabled, signal) => { await wait(signal); return { ...row, enabled: false }; };
  f.adapter.remove = async (_id, signal) => { await wait(signal); };
  const view = await mount(<ChannelSetup adapter={f.adapter} />); const user = userEvent.setup(); await screen.findByRole('heading', { name: '原渠道' });
  if (action === 'test') await user.click(screen.getByRole('button', { name: '向 原渠道 发送测试' }));
  if (action === 'toggle') await user.click(screen.getByRole('button', { name: '停用' }));
  if (action === 'remove') { await user.click(screen.getByRole('button', { name: '删除' })); await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: '删除' })); }
  await waitFor(() => expect(calls).toBe(1)); const reads = f.list.mock.calls.length;
  await relogin(view); await act(async () => pending.resolve());
  expect(signals[0]?.aborted).toBe(true); expect(f.list).toHaveBeenCalledTimes(reads); expect(calls).toBe(1);
  if (action === 'test') expect(screen.queryByText('发送成功')).toBeNull();
});

test('旧会话编辑读取迟到，不能替换新会话已打开的新建草稿', async () => {
  const f = adapterFixture(); const pending = deferred<ChannelRow>(); const signals: AbortSignal[] = []; let calls = 0;
  f.adapter.get = async (_id, signal) => { calls++; signals.push(signal); return pending.promise; };
  const view = await mount(<ChannelSetup adapter={f.adapter} />); const user = userEvent.setup(); await user.click(await screen.findByRole('button', { name: '编辑 原渠道' })); await relogin(view);
  try {
    const add = screen.getByRole<HTMLButtonElement>('button', { name: '添加渠道' }); expect(add.disabled).toBe(false); await user.click(add);
    const name = screen.getByRole<HTMLInputElement>('textbox', { name: '名称' }); await user.type(name, '新登录草稿');
    await act(async () => pending.resolve({ ...row, name: '旧编辑结果' }));
    expect(signals[0]?.aborted).toBe(true); expect(name.value).toBe('新登录草稿'); expect(screen.queryByRole('heading', { name: '编辑渠道：旧编辑结果' })).toBeNull(); expect(calls).toBe(1);
  } finally { await act(async () => pending.resolve(row)); }
});

test('旧会话规则保存迟到不覆盖新草稿或缓存，恢复后只显式保存一次', async () => {
  const pending = deferred<Response>(); const signals: AbortSignal[] = []; let calls = 0;
  const fetch: ApiFetch = async (request) => { if (request.method !== 'PATCH') return readFixture(request); calls++; signals.push(request.signal); if (calls === 1) return pending.promise; return Response.json({ ...snapshot, values: { ...snapshot.values, 'notify.max_per_run': 9 } }); };
  const view = await mount(<NotificationsPage />, fetch); const user = userEvent.setup(); const maximum = await screen.findByRole<HTMLInputElement>('textbox', { name: '每次推送总数上限' });
  await user.clear(maximum); await user.type(maximum, '7'); await user.click(screen.getByRole('button', { name: '保存通知规则' })); await relogin(view);
  try {
    expect(maximum.disabled).toBe(false); await user.clear(maximum); await user.type(maximum, '9');
    await act(async () => pending.resolve(Response.json({ ...snapshot, values: { ...snapshot.values, 'notify.max_per_run': 7 } })));
    expect(signals[0]?.aborted).toBe(true); expect(maximum.value).toBe('9'); expect(view.queryClient.getQueryData<components['schemas']['SettingsSnapshot']>(apiKey('settings'))?.values['notify.max_per_run']).toBe(5); expect(screen.queryByText('通知规则已保存。')).toBeNull(); expect(calls).toBe(1);
    await user.click(screen.getByRole('button', { name: '保存通知规则' })); await screen.findByText('通知规则已保存。'); expect(calls).toBe(2); expect(maximum.value).toBe('9');
  } finally { await act(async () => pending.resolve(Response.json(snapshot))); }
});

for (const page of ['channel', 'rules'] as const) test(`${page}未保存内容接根级唯一确认，取消离开保留草稿`, async () => {
  const f = adapterFixture(); const element = page === 'channel' ? <ChannelEditor channel={row} adapter={f.adapter} onSaved={() => {}} onCancel={() => {}} /> : <NotificationsPage />;
  const view = await mount(element); const user = userEvent.setup(); const label = page === 'channel' ? '名称' : '每次推送总数上限';
  const input = await screen.findByRole<HTMLInputElement>('textbox', { name: label }); await user.clear(input); await user.type(input, page === 'channel' ? '保留草稿' : '8');
  await user.click(screen.getByRole('link', { name: '去目标页' })); const dialog = await screen.findByRole('alertdialog', { name: '放弃未保存的修改？' }); expect(screen.getAllByRole('alertdialog')).toHaveLength(1);
  await user.click(within(dialog).getByRole('button', { name: '取消' })); expect(input.value).toBe(page === 'channel' ? '保留草稿' : '8'); expect(view.router.state.location.pathname).toBe('/settings/notifications');
  await user.click(screen.getByRole('link', { name: '去目标页' })); await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: '放弃修改并离开' })); await screen.findByRole('heading', { name: '测试目标页' }); expect(view.router.state.location.pathname).toBe('/overview');
});
