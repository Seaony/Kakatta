import { expect, test } from 'bun:test';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { useEffect, useState, type Dispatch, type SetStateAction } from 'react';
import { createApiClient, type ApiFetch } from '../../api/client';
import { ApiProvider } from '../../api/hooks';
import type { components } from '../../api/schema.gen';
import { AuthenticationContext } from '../../app/auth-state';
import { createI18n, I18nProvider, type Locale } from '../../i18n';
import { ChannelSetup } from './channels';

const channel: components['schemas']['ChannelTelegram'] = { id: 1, type: 'telegram', name: '本地渠道', enabled: true, config: { chat_id: 'local-fixture' }, secret_set: { token: true }, last_test_at: null, last_test_ok: null, last_error: null };
type Task = Pick<components['schemas']['TaskView'], 'id' | 'name' | 'channel_ids'>;
const page = (items: Task[], cursor: string | null = null) => Response.json({ items, next_cursor: cursor, total: items.length });
const failure = (status: number, code = 'http.unavailable') => Response.json({ error: { code, args: {} }, fields: [], request_id: 'local-impact' }, { status });
function deferred<Value>() {
  let finish: ((value: Value) => void) | undefined;
  const promise = new Promise<Value>((resolve) => { finish = resolve; });
  return { promise, resolve(value: Value) { if (!finish) throw new Error('测试等待尚未建立'); finish(value); } };
}
async function mount(transport: ApiFetch, locale: Locale = 'zh-CN') {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } } });
  const api = createApiClient({ fetch: async (request) => {
    const path = new URL(request.url).pathname;
    if (path === '/api/v1/settings') return Response.json({ values: { 'general.timezone': 'UTC' }, sources: {}, warnings: [] });
    if (path === '/api/v1/channels') return Response.json([channel]);
    return transport(request);
  } });
  let updateAuthentication: Dispatch<SetStateAction<boolean>> | undefined;
  function Session() {
    const [authenticated, setAuthenticated] = useState(true);
    updateAuthentication = setAuthenticated;
    useEffect(() => api.subscribeAuthenticationLost(() => setAuthenticated(false)), []);
    return <AuthenticationContext.Provider value={{ authenticated, user: undefined, complete: () => {}, refresh: () => {} }}><ChannelSetup /></AuthenticationContext.Provider>;
  }
  const root = createRootRoute({ component: Session });
  const router = createRouter({ routeTree: root, history: createMemoryHistory({ initialEntries: ['/'] }) });
  await act(async () => { await router.load(); });
  const view = render(<I18nProvider instance={createI18n(locale)}><ApiProvider api={api}><QueryClientProvider client={queryClient}><RouterProvider router={router} /></QueryClientProvider></ApiProvider></I18nProvider>);
  return { ...view, async authenticate(value: boolean) { await act(async () => { if (!updateAuthentication) throw new Error('测试会话尚未挂载'); updateAuthentication(value); }); } };
}

for (const locale of ['zh-CN', 'en'] as const) test(`${locale}删除前读完第二页并逐个列出影响，最后系统渠道冲突保留确认`, async () => {
  const second = deferred<Response>();
  const reads: URL[] = [];
  let deletes = 0;
  await mount(async (request) => {
    if (request.method === 'DELETE') { deletes++; return failure(409, 'channels.system_target_required'); }
    const url = new URL(request.url); expect(url.pathname).toBe('/api/v1/tasks'); reads.push(url);
    if (reads.length === 1) return page(Array.from({ length: 200 }, (_, id) => ({ id: id + 1, name: id === 5 ? '还有其他渠道' : `无关任务${id}`, channel_ids: id === 5 ? [1, 2] : [2] })), 'second');
    return second.promise;
  }, locale);
  const user = userEvent.setup(); const remove = locale === 'zh-CN' ? '删除' : 'Delete';
  await user.click(await screen.findByRole('button', { name: remove }));
  const dialog = screen.getByRole('alertdialog');
  await waitFor(() => expect(reads).toHaveLength(2));
  expect(reads.map((url) => url.searchParams.get('limit'))).toEqual(['200', '200']);
  expect(reads.map((url) => url.searchParams.get('cursor'))).toEqual([null, 'second']);
  expect(within(dialog).getByRole<HTMLButtonElement>('button', { name: remove }).disabled).toBe(true);
  expect(within(dialog).queryByText('还有其他渠道')).toBeNull(); expect(deletes).toBe(0);
  await act(async () => second.resolve(page([{ id: 201, name: '最后一个渠道', channel_ids: [1] }])));
  const last = await within(dialog).findByText('最后一个渠道');
  const warning = locale === 'zh-CN' ? '删除后没有推送渠道。' : 'No notification channels will remain after deletion.';
  expect(within(dialog).getAllByRole('listitem')).toHaveLength(2);
  expect(last.closest('li')?.textContent).toContain(warning);
  expect(within(dialog).getByText('还有其他渠道').closest('li')?.textContent).not.toContain(warning);
  expect(within(dialog).queryByText('无关任务0')).toBeNull();
  await user.click(within(dialog).getByRole('button', { name: remove }));
  await within(dialog).findByRole('alert'); expect(deletes).toBe(1);
  expect(screen.getByRole('alertdialog')).toBe(dialog);
});

for (const locale of ['zh-CN', 'en'] as const) test(`${locale}无任务受影响时明确说明并允许显式删除`, async () => {
  let deletes = 0;
  await mount(async (request) => {
    if (request.method === 'DELETE') { deletes++; return Response.json({ id: 1, affected_task_ids: [] }); }
    expect(new URL(request.url).pathname).toBe('/api/v1/tasks'); return page([{ id: 2, name: '其他任务', channel_ids: [2] }]);
  }, locale);
  const user = userEvent.setup(); const remove = locale === 'zh-CN' ? '删除' : 'Delete';
  await user.click(await screen.findByRole('button', { name: remove })); const dialog = screen.getByRole('alertdialog');
  await within(dialog).findByText(locale === 'zh-CN' ? '没有任务使用这个渠道。' : 'No tasks use this channel.');
  expect(within(dialog).queryByRole('list')).toBeNull(); expect(deletes).toBe(0);
  await user.click(within(dialog).getByRole('button', { name: remove }));
  await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull()); expect(deletes).toBe(1);
});

test('后页失败不把部分结果当完整影响，重试只重新读取而不删除', async () => {
  let reads = 0, deletes = 0;
  await mount(async (request) => {
    if (request.method === 'DELETE') { deletes++; return Response.json({ id: 1, affected_task_ids: [2] }); }
    reads++;
    if (reads === 1) return page([{ id: 1, name: '未完成的部分结果', channel_ids: [1] }], 'next');
    if (reads === 2) return failure(503);
    return page([{ id: 2, name: '重试后完整结果', channel_ids: [1, 2] }]);
  });
  const user = userEvent.setup(); await user.click(await screen.findByRole('button', { name: '删除' })); const dialog = screen.getByRole('alertdialog');
  await within(dialog).findByRole('alert'); expect(reads).toBe(2); expect(deletes).toBe(0);
  expect(within(dialog).queryByText('未完成的部分结果')).toBeNull(); expect(within(dialog).queryByRole('button', { name: '删除' })).toBeNull();
  await user.click(within(dialog).getByRole('button', { name: '重试' }));
  await within(dialog).findByText('重试后完整结果'); expect(reads).toBe(3); expect(deletes).toBe(0);
  expect(within(dialog).getByRole<HTMLButtonElement>('button', { name: '删除' }).disabled).toBe(false);
});

test('关闭确认取消读取，重新打开重新读，忽略取消的迟到结果不能覆盖', async () => {
  const pending = deferred<Response>(); const signals: AbortSignal[] = []; let reads = 0;
  await mount(async (request) => {
    expect(request.method).toBe('GET'); signals.push(request.signal); reads++;
    return reads === 1 ? pending.promise : page([]);
  });
  const user = userEvent.setup(); await user.click(await screen.findByRole('button', { name: '删除' }));
  await waitFor(() => expect(reads).toBe(1));
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: '取消' }));
  await waitFor(() => expect(signals[0]?.aborted).toBe(true));
  await user.click(screen.getByRole('button', { name: '删除' })); await within(screen.getByRole('alertdialog')).findByText('没有任务使用这个渠道。');
  await act(async () => pending.resolve(page([{ id: 7, name: '旧请求任务', channel_ids: [1] }])));
  expect(screen.queryByText('旧请求任务')).toBeNull(); expect(reads).toBe(2);
});

test('失去认证关闭确认并取消读取，迟到结果不重开弹框', async () => {
  const pending = deferred<Response>(); let observed: AbortSignal | undefined;
  const view = await mount(async (request) => { expect(request.method).toBe('GET'); observed = request.signal; return pending.promise; });
  const user = userEvent.setup(); await user.click(await screen.findByRole('button', { name: '删除' }));
  await waitFor(() => expect(observed).toBeDefined()); await view.authenticate(false);
  await waitFor(() => expect(observed?.aborted).toBe(true)); expect(screen.queryByRole('alertdialog')).toBeNull();
  await act(async () => pending.resolve(page([{ id: 8, name: '旧会话任务', channel_ids: [1] }])));
  expect(screen.queryByText('旧会话任务')).toBeNull(); expect(screen.queryByRole('alertdialog')).toBeNull();
});

test('影响读取401按统一认证失效关闭，不自动重试或删除', async () => {
  let reads = 0, deletes = 0;
  await mount(async (request) => {
    if (request.method === 'DELETE') deletes++;
    reads++; return failure(401, 'auth.unauthenticated');
  });
  const user = userEvent.setup(); await user.click(await screen.findByRole('button', { name: '删除' }));
  await waitFor(() => { expect(reads).toBe(1); expect(screen.queryByRole('alertdialog')).toBeNull(); });
  await user.click(screen.getByRole('button', { name: '删除' })); expect(reads).toBe(1); expect(deletes).toBe(0);
});
