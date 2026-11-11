import { afterEach, expect, spyOn, test } from 'bun:test';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { createApiClient, type ApiFetch } from '../../api/client';
import { ApiProvider } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import type { Schema } from '../../api/types';
import { createAppQueryClient } from '../../app/query-client';
import type { LogsSearch } from '../../app/search';
import { createI18n, I18nProvider } from '../../i18n';
import { LogsPage } from './logs-page';

const now = Date.UTC(2026, 10, 11, 4);
const clients: ReturnType<typeof createAppQueryClient>[] = [];
const restorers: (() => void)[] = [];
afterEach(() => {
  for (const client of clients.splice(0)) client.clear();
  for (const restore of restorers.splice(0)) restore();
});

function run(overrides: Partial<Schema<'RunView'>> = {}): Schema<'RunView'> {
  return {
    id: 7, task_id: 3, task_name: '本地相机任务', trigger: 'auto', account_id: 4,
    account_name: '历史账号', proxy_name: '历史代理', started_at: now - 5000, ended_at: now - 1000,
    outcome: 'success', failure_kind: null, message: { code: 'run.no_new_items', args: {} }, repeat_count: 1,
    page_count: 1, scanned_count: 20, new_count: 0, layer1_rejected: 0, layer2_count: null,
    layer2_rejected: null, detail_count: 0, seller_page_count: 0, analyzed_count: 0,
    recommended_count: 0, notified_count: 0, warnings: [], ...overrides,
  };
}

const overview: Schema<'RunLogOverview'> = {
  as_of: now, from_at: now - 86400000, to_at: now, total_count: 10, success_count: 2,
  failed_count: 1, skipped_count: 7, average_duration_ms: 1500, success_rate: 2 / 3,
  coverage: { from_at: now - 86400000, to_at: now, is_partial: false },
  buckets: Array.from({ length: 24 }, (_, index) => ({
    from_at: now - 86400000 + index * 3600000, to_at: now - 86400000 + (index + 1) * 3600000,
    success_count: index === 0 ? 2 : 0, failed_count: index === 1 ? 1 : 0, skipped_count: index === 2 ? 7 : 0,
  })),
};

function page(items: Schema<'RunView'>[] = [run()], next: string | null = null): Schema<'PageRunView'> {
  return { items, next_cursor: next, total: items.length };
}

function steps(start: number, end: number): Schema<'RunStep'>[] {
  return Array.from({ length: end - start + 1 }, (_, index) => ({
    run_id: 7, seq: start + index, at: now - 2000, status: start + index > 100 ? 'fail' : 'ok',
    message: { code: start + index === 110 ? 'run.timeout' : 'run.browser_operation', args: {} }, detail: null,
  }));
}

type Handler = (request: Request, url: URL) => Response | Promise<Response> | undefined;
function setup(handle?: Handler, initial: LogsSearch = {}, locale: 'zh-CN' | 'en' = 'zh-CN') {
  const requests: Request[] = [];
  const transport: ApiFetch = async (request) => {
    requests.push(request);
    const url = new URL(request.url);
    const response = await handle?.(request, url);
    if (response) return response;
    if (url.pathname === '/api/v1/settings') return Response.json({ values: { 'general.timezone': 'Asia/Shanghai', 'retention.run_steps_days': 14, 'retention.stats_days': 31 }, sources: {}, warnings: [] } satisfies Schema<'SettingsSnapshot'>);
    if (url.pathname === '/api/v1/runs/overview') return Response.json(overview);
    if (url.pathname === '/api/v1/runs') return Response.json(page());
    if (url.pathname === '/api/v1/runs/7') return Response.json({ run: run(), steps: { items: [], next_cursor: null, total: 0 } } satisfies Schema<'RunDetail'>);
    if (url.pathname === '/api/v1/tasks') return Response.json({ items: [], next_cursor: null, total: 0 } satisfies Schema<'PageTaskView'>);
    if (url.pathname === '/api/v1/system/events') return Response.json({ items: [], next_cursor: null, total: 0 } satisfies Schema<'PageSystemEvent'>);
    throw new Error(`本地测试未定义请求 ${url.pathname}`);
  };
  const api = createApiClient({ fetch: transport });
  const client = createAppQueryClient();
  clients.push(client);
  function TestPage() { const [search, setSearch] = useState(initial); return <LogsPage search={search} onSearchChange={setSearch} />; }
  const route = createRootRoute({ component: TestPage });
  const router = createRouter({ routeTree: route, history: createMemoryHistory({ initialEntries: ['/'] }) });
  const rendered = render(<I18nProvider instance={createI18n(locale)}><QueryClientProvider client={client}><ApiProvider api={api}><RouterProvider router={router} /></ApiProvider></QueryClientProvider></I18nProvider>);
  return { requests, client, api, ...rendered };
}

const logsText = 'record_type\trun_id\tmessage\nrun\t7\t历史相机\nstep\t7\t响应超时\n';

function exportResponse(disposition = 'attachment; filename=kakatta-runs-2026-11-11.txt', mediaType = 'text/plain; charset=utf-8') {
  return new Response(logsText, { headers: { 'Content-Type': mediaType, 'Content-Disposition': disposition } });
}

function captureDownloads() {
  const blobs: Blob[] = [];
  const names: string[] = [];
  const createURL = spyOn(URL, 'createObjectURL').mockImplementation((object) => {
    if (object instanceof Blob) blobs.push(object);
    return 'blob:http://localhost/run-export-test';
  });
  const click = spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
    names.push(this.download);
  });
  restorers.push(() => { click.mockRestore(); createURL.mockRestore(); });
  return { blobs, names };
}

async function openExport() {
  await userEvent.setup().click(await screen.findByRole('button', { name: '导出日志' }));
  return screen.getByRole('dialog', { name: '导出日志' });
}

test('24小时使用后台总数与空值，未结束占位不显示失败，展开保留账号代理快照', async () => {
  setup((_request, url) => {
    if (url.pathname === '/api/v1/runs/overview') return Response.json({ ...overview, success_rate: null, average_duration_ms: null, coverage: { ...overview.coverage, is_partial: true } } satisfies Schema<'RunLogOverview'>);
    if (url.pathname === '/api/v1/runs') return Response.json(page([run({ ended_at: null, outcome: null, failure_kind: null, message: null })]));
    return undefined;
  });
  await screen.findByRole('button', { name: '展开运行 #7' });
  expect(screen.getByText('检查中')).toBeTruthy();
  const summary = screen.getByRole('region', { name: '过去 24 小时' });
  expect(summary.textContent).toContain('10');
  expect(within(summary).getAllByText('—')).toHaveLength(2);
  expect(summary.textContent).toContain('统计覆盖不完整');
  await userEvent.setup().click(screen.getByRole('button', { name: '展开运行 #7' }));
  expect(await screen.findByRole('link', { name: '历史账号' })).toBeTruthy();
  expect(screen.getByText('历史代理')).toBeTruthy();
  await userEvent.setup().click(screen.getByText('查看每小时数据表'));
  expect(screen.getByRole('table', { name: '每小时运行结果' }).querySelectorAll('tbody tr')).toHaveLength(24);
});

test('步骤持续分页，100步之后的全部失败仍可查看', async () => {
  const { requests } = setup((_request, url) => {
    if (url.pathname === '/api/v1/runs/7') return Response.json({ run: run(), steps: { items: steps(1, 50), next_cursor: 'step-50', total: 110 } } satisfies Schema<'RunDetail'>);
    if (url.pathname === '/api/v1/runs/7/steps') return Response.json(url.searchParams.get('cursor') === 'step-50'
      ? { items: steps(51, 100), next_cursor: 'step-100', total: 110 } satisfies Schema<'PageRunStep'>
      : { items: steps(101, 110), next_cursor: null, total: 110 } satisfies Schema<'PageRunStep'>);
    return undefined;
  });
  await userEvent.setup().click(await screen.findByRole('button', { name: '展开运行 #7' }));
  await userEvent.setup().click(await screen.findByRole('button', { name: '加载更多步骤' }));
  await waitFor(() => expect(screen.getByRole('region', { name: '逐步日志' }).querySelectorAll('li')).toHaveLength(100));
  await userEvent.setup().click(screen.getByRole('button', { name: '加载更多步骤' }));
  await screen.findByText('本次运行超时');
  expect(screen.getByRole('region', { name: '逐步日志' }).querySelectorAll('li')).toHaveLength(110);
  expect(screen.queryByRole('button', { name: '加载更多步骤' })).toBeNull();
  expect(requests.filter((request) => new URL(request.url).pathname.endsWith('/steps')).map((request) => new URL(request.url).searchParams.get('cursor'))).toEqual(['step-50', 'step-100']);
});

test('缩短保留期时重新读取已展开的步骤，切回旧保留期不会复用旧分页', async () => {
  let days = 14;
  let cleared = false;
  const { client, requests } = setup((_request, url) => {
    if (url.pathname === '/api/v1/runs/7') return Response.json({
      run: run(),
      steps: days === 14 && !cleared ? { items: steps(1, 50), next_cursor: 'step-50', total: 51 } : { items: [], next_cursor: null, total: 0 },
    } satisfies Schema<'RunDetail'>);
    if (url.pathname === '/api/v1/runs/7/steps') return Response.json({ items: steps(51, 51), next_cursor: null, total: 51 } satisfies Schema<'PageRunStep'>);
    return undefined;
  });
  const user = userEvent.setup();
  await user.click(await screen.findByRole('button', { name: '展开运行 #7' }));
  await user.click(await screen.findByRole('button', { name: '加载更多步骤' }));
  await waitFor(() => expect(screen.getByRole('region', { name: '逐步日志' }).querySelectorAll('li')).toHaveLength(51));
  const saveRetention = async (next: number) => {
    days = next;
    await act(async () => {
      client.setQueryData<Schema<'SettingsSnapshot'>>(apiKey('settings'), (snapshot) => snapshot && {
        ...snapshot, values: { ...snapshot.values, 'retention.run_steps_days': days },
      });
    });
  };
  await saveRetention(1);
  expect(await screen.findByText(/这次运行暂无可用逐步日志/)).toBeTruthy();
  expect(screen.getByRole('region', { name: '逐步日志' }).querySelectorAll('li')).toHaveLength(0);
  expect(screen.getByText('历史代理')).toBeTruthy();
  cleared = true;
  await saveRetention(14);
  await waitFor(() => expect(requests.filter((request) => new URL(request.url).pathname === '/api/v1/runs/7')).toHaveLength(3));
  const reloaded = await screen.findByRole('region', { name: '逐步日志' });
  expect(within(reloaded).getByText(/这次运行暂无可用逐步日志/)).toBeTruthy();
  expect(reloaded.querySelectorAll('li')).toHaveLength(0);
});

for (const language of [
  { locale: 'zh-CN', expand: '展开运行 #7', steps: '逐步日志', events: '需要处理的异常', account: '查看账号', normal: '没有发现新商品', marker: '旧版本消息' },
  { locale: 'en', expand: 'Expand run #7', steps: 'Step logs', events: 'Issues needing attention', account: 'View account', normal: 'No new items were found', marker: 'Legacy message' },
] as const) {
  test(`历史消息保留原因码、正常文案和记录行 ${language.locale}`, async () => {
    const oldRun = run({
      task_name: '旧运行',
      message: { code: 'run.old_scan_complete', args: { obsolete: '仅存储的旧参数' } },
      warnings: [{ code: 'run.failure_pause', args: { task: '旧运行', count: 3 } }],
    });
    const oldStep: Schema<'RunStep'> = {
      run_id: 7, seq: 1, at: now - 2000, status: 'warn',
      message: { code: 'run.old_step', args: {} }, detail: { code: 'run.hourly_quota', args: { limit: 20 } },
    };
    const event: Schema<'SystemEvent'> = {
      id: 5, kind: 'account_verify', subject_id: 4,
      message: { code: 'event.old_verification', args: {} }, created_at: now, resolved_at: null, notified_at: null,
    };
    setup((_request, url) => {
      if (url.pathname === '/api/v1/runs') return Response.json(page([oldRun, run({ id: 8, task_name: '正常运行' })]));
      if (url.pathname === '/api/v1/runs/7') {
        return Response.json({ run: oldRun, steps: { items: [oldStep], next_cursor: null, total: 1 } } satisfies Schema<'RunDetail'>);
      }
      if (url.pathname === '/api/v1/system/events') {
        return Response.json({ items: [event], next_cursor: null, total: 1 } satisfies Schema<'PageSystemEvent'>);
      }
      return undefined;
    }, {}, language.locale);
    const oldHeading = await screen.findByRole('heading', { name: '旧运行' });
    expect(oldHeading.closest('li')?.textContent).toContain('run.old_scan_complete');
    expect(oldHeading.closest('li')?.textContent).not.toContain('仅存储的旧参数');
    const normalRow = screen.getByRole('heading', { name: '正常运行' }).closest('li');
    expect(normalRow?.textContent).toContain(language.normal);
    expect(normalRow?.textContent).not.toContain(language.marker);
    await userEvent.setup().click(screen.getByRole('button', { name: language.expand }));
    const detail = await screen.findByRole('region', { name: language.steps });
    expect(screen.getAllByText('run.old_scan_complete')).toHaveLength(2);
    expect(screen.getByText('run.failure_pause')).toBeTruthy();
    expect(detail.textContent).toContain('run.old_step');
    expect(detail.textContent).toContain('run.hourly_quota');
    expect(detail.querySelectorAll('li')).toHaveLength(1);
    const events = screen.getByRole('region', { name: language.events });
    expect(events.textContent).toContain('event.old_verification');
    const recovery = await within(events).findByRole('link', { name: language.account });
    expect(recovery.getAttribute('href')).toContain('account_id=4');
    expect(screen.getAllByText(language.marker)).toHaveLength(6);
  });
}

test('筛选用真实UTC参数，失败保留输入与字段错误和请求编号', async () => {
  const { requests } = setup((_request, url) => url.pathname === '/api/v1/runs' && url.searchParams.has('q') ? Response.json({ error: { code: 'http.unavailable', args: {} }, fields: [{ key: 'q', reason: { code: 'settings.invalid_option', args: {} } }], request_id: 'logs-request-42' }, { status: 503 }) : undefined, { task_id: 3, from_at: Date.UTC(2026, 10, 10, 1, 0, 0, 123) });
  await screen.findByRole('button', { name: '展开运行 #7' });
  fireEvent.change(screen.getByRole('textbox', { name: '搜索日志' }), { target: { value: '相机_100%' } });
  fireEvent.change(screen.getByLabelText('结束时间（UTC，不含）'), { target: { value: '2026-11-11T01:00' } });
  fireEvent.submit(screen.getByRole('button', { name: '应用筛选' }).closest('form')!);
  await waitFor(() => expect(requests.some((request) => new URL(request.url).searchParams.get('q') === '相机_100%')).toBe(true));
  await screen.findByText(/logs-request-42/);
  expect((screen.getByRole('textbox', { name: '搜索日志' }) as HTMLInputElement).value).toBe('相机_100%');
  expect(screen.getByRole('textbox', { name: '搜索日志' }).getAttribute('aria-invalid')).toBe('true');
  const filtered = requests.map((request) => new URL(request.url)).find((url) => url.searchParams.get('q') === '相机_100%');
  expect(filtered?.searchParams.get('task_id')).toBe('3');
  expect(filtered?.searchParams.get('from_at')).toBe(String(Date.UTC(2026, 10, 10, 1, 0, 0, 123)));
  expect(filtered?.searchParams.get('to_at')).toBe(String(Date.UTC(2026, 10, 11, 1)));
});

test('错误时间范围不请求，组合输入不触发搜索', async () => {
  const { requests } = setup();
  await screen.findByRole('button', { name: '展开运行 #7' });
  const input = screen.getByRole('textbox', { name: '搜索日志' });
  const before = requests.length;
  fireEvent.compositionStart(input);
  fireEvent.change(input, { target: { value: '测试' } });
  fireEvent.keyDown(input, { key: 'Enter', isComposing: true });
  fireEvent.compositionEnd(input);
  expect(requests.length).toBe(before);
  fireEvent.change(screen.getByLabelText('开始时间（UTC）'), { target: { value: '2026-11-11T01:00' } });
  fireEvent.change(screen.getByLabelText('结束时间（UTC，不含）'), { target: { value: '2026-11-10T01:00' } });
  fireEvent.submit(screen.getByRole('button', { name: '应用筛选' }).closest('form')!);
  expect(screen.getByText('结束时间必须晚于开始时间。')).toBeTruthy();
  expect(document.activeElement).toBe(screen.getByLabelText('结束时间（UTC，不含）'));
  expect(requests.length).toBe(before);
});

test('日志下载沿用六项已应用筛选并保留毫秒和附件原始字节', async () => {
  const downloads = captureDownloads();
  const { requests } = setup((_request, url) => url.pathname === '/api/v1/runs/export' ? exportResponse() : undefined, {
    task_id: 3, outcome: 'failed', failure_kind: 'timeout', q: '相机_100%',
    from_at: now - 8001, to_at: now - 123, limit: 9, cursor: '当前页游标', run_id: 7,
  });
  await screen.findByText('本地相机任务');
  fireEvent.change(screen.getByRole('textbox', { name: '搜索日志' }), { target: { value: '未应用草稿' } });
  const dialog = await openExport();
  expect(dialog.textContent).toContain('全部运行记录和可用逐步日志');
  expect(dialog.textContent).toContain('相机_100%');
  expect(dialog.textContent).not.toContain('未应用草稿');
  expect(downloads.names).toEqual([]);
  await userEvent.setup().click(within(dialog).getByRole('button', { name: '导出日志' }));
  await within(dialog).findByText('下载已开始');
  expect(downloads.names).toEqual(['kakatta-runs-2026-11-11.txt']);
  expect(await downloads.blobs[0]?.text()).toBe(logsText);
  const request = requests.find((item) => new URL(item.url).pathname === '/api/v1/runs/export');
  expect(request?.method).toBe('GET');
  expect(request?.body).toBeNull();
  expect(Object.fromEntries(new URL(request!.url).searchParams)).toEqual({
    task_id: '3', outcome: 'failed', failure_kind: 'timeout', q: '相机_100%',
    from_at: String(now - 8001), to_at: String(now - 123),
  });
});

test('503保留导出筛选与请求编号，非文本响应被拦住，不安全附件名回退', async () => {
  const downloads = captureDownloads();
  let attempts = 0;
  setup((_request, url) => {
    if (url.pathname !== '/api/v1/runs/export') return undefined;
    attempts++;
    if (attempts === 1) return Response.json({ error: { code: 'http.unavailable', args: {} }, fields: [], request_id: 'export-unavailable' }, { status: 503 });
    if (attempts === 2) return exportResponse(undefined, 'text/html');
    return exportResponse('attachment; filename="../unsafe.txt"');
  }, { q: '保留筛选' });
  const dialog = await openExport();
  const user = userEvent.setup();
  await user.click(within(dialog).getByRole('button', { name: '导出日志' }));
  await within(dialog).findByText(/export-unavailable/);
  expect(dialog.textContent).toContain('保留筛选');
  expect(attempts).toBe(1);
  expect(downloads.names).toEqual([]);
  await user.click(within(dialog).getByRole('button', { name: '重试' }));
  await within(dialog).findByText(/服务端返回了无法读取的响应/);
  expect(downloads.names).toEqual([]);
  await user.click(within(dialog).getByRole('button', { name: '重试' }));
  await within(dialog).findByText('下载已开始');
  expect(downloads.names).toEqual(['kakatta-runs.txt']);
});

test('取消中止导出，旧响应迟到不会下载或覆盖随后发起的请求', async () => {
  const downloads = captureDownloads();
  const pending: { request: Request; complete(response: Response): void }[] = [];
  setup((request, url) => url.pathname === '/api/v1/runs/export'
    ? new Promise<Response>((complete) => pending.push({ request, complete })) : undefined);
  const dialog = await openExport();
  const user = userEvent.setup();
  await user.dblClick(within(dialog).getByRole('button', { name: '导出日志' }));
  expect(pending).toHaveLength(1);
  await user.click(within(dialog).getByRole('button', { name: '取消' }));
  expect(pending[0]?.request.signal.aborted).toBe(true);
  expect(within(dialog).getByText('已取消下载请求')).toBeTruthy();
  await user.click(within(dialog).getByRole('button', { name: '导出日志' }));
  expect(pending).toHaveLength(2);
  await act(async () => { pending[0]?.complete(exportResponse()); });
  expect(downloads.names).toEqual([]);
  expect(within(dialog).getByRole('button', { name: '正在准备下载' }).getAttribute('aria-busy')).toBe('true');
  await act(async () => { pending[1]?.complete(exportResponse('attachment; filename=kakatta-runs-2026-11-12.txt')); });
  await within(dialog).findByText('下载已开始');
  expect(downloads.names).toEqual(['kakatta-runs-2026-11-12.txt']);
});

test('401由共享客户端通知会话失效，关闭导出弹窗并保留筛选', async () => {
  const downloads = captureDownloads();
  const { api, requests } = setup((_request, url) => url.pathname === '/api/v1/runs/export'
    ? Response.json({ error: { code: 'auth.unauthenticated', args: {} }, fields: [], request_id: 'export-expired' }, { status: 401 }) : undefined, { q: '登录后保留' });
  let lost = 0;
  restorers.push(api.subscribeAuthenticationLost(() => { lost++; }));
  const dialog = await openExport();
  await userEvent.setup().click(within(dialog).getByRole('button', { name: '导出日志' }));
  await waitFor(() => expect(screen.queryByRole('dialog', { name: '导出日志' })).toBeNull());
  expect(lost).toBe(1);
  expect(downloads.names).toEqual([]);
  expect(requests.filter((request) => new URL(request.url).pathname === '/api/v1/runs/export')).toHaveLength(1);
  expect((screen.getByRole('textbox', { name: '搜索日志' }) as HTMLInputElement).value).toBe('登录后保留');
});

test('未解决异常请求resolved=false，恢复入口只导航到对应账号', async () => {
  const event: Schema<'SystemEvent'> = { id: 5, kind: 'account_verify', subject_id: 4, message: { code: 'run.risk_verify', args: {} }, created_at: now, resolved_at: null, notified_at: null };
  const { requests } = setup((_request, url) => url.pathname === '/api/v1/system/events' ? Response.json({ items: [event], next_cursor: null, total: 1 } satisfies Schema<'PageSystemEvent'>) : undefined);
  const link = await screen.findByRole('link', { name: '查看账号' });
  expect(link.getAttribute('href')).toContain('account_id=4');
  const request = requests.find((item) => new URL(item.url).pathname === '/api/v1/system/events');
  expect(request && new URL(request.url).searchParams.get('resolved')).toBe('false');
  expect(requests.every((item) => item.method === 'GET')).toBe(true);
});

test('新筛选取消旧请求，等待请求时保留已有内容', async () => {
  let oldSignal: AbortSignal | undefined;
  const { requests } = setup((request, url) => {
    if (url.pathname === '/api/v1/runs' && url.searchParams.get('q') === 'slow') {
      oldSignal = request.signal;
      return new Promise<Response>((_resolve, reject) => request.signal.addEventListener('abort', () => reject(new DOMException('取消', 'AbortError')), { once: true }));
    }
    return undefined;
  });
  await screen.findByRole('button', { name: '展开运行 #7' });
  const input = screen.getByRole('textbox', { name: '搜索日志' });
  fireEvent.change(input, { target: { value: 'slow' } });
  fireEvent.submit(screen.getByRole('button', { name: '应用筛选' }).closest('form')!);
  await waitFor(() => expect(oldSignal).toBeDefined());
  expect(screen.getByText('本地相机任务')).toBeTruthy();
  fireEvent.change(input, { target: { value: 'fast' } });
  fireEvent.submit(screen.getByRole('button', { name: '应用筛选' }).closest('form')!);
  await waitFor(() => expect(oldSignal?.aborted).toBe(true));
  await waitFor(() => expect(requests.some((request) => new URL(request.url).searchParams.get('q') === 'fast')).toBe(true));
});

test('列表游标翻页后可返回，刷新失败不清空当前记录', async () => {
  let fail = false;
  const { requests } = setup((_request, url) => {
    if (url.pathname !== '/api/v1/runs') return undefined;
    if (fail) return Response.json({ error: { code: 'http.unavailable', args: {} }, fields: [], request_id: 'refresh-failed' }, { status: 503 });
    return Response.json(url.searchParams.get('cursor') ? page([run({ id: 8, task_name: '第二页记录' })]) : page([run()], 'next-runs'));
  });
  await screen.findByRole('button', { name: '展开运行 #7' });
  await userEvent.setup().click(screen.getByRole('button', { name: '下一页' }));
  await screen.findByText('第二页记录');
  expect(requests.some((request) => new URL(request.url).searchParams.get('cursor') === 'next-runs')).toBe(true);
  await userEvent.setup().click(screen.getByRole('button', { name: '上一页' }));
  await screen.findByText('本地相机任务');
  fail = true;
  await userEvent.setup().click(screen.getByRole('button', { name: '刷新' }));
  await screen.findByText(/refresh-failed/);
  expect(screen.getByText('本地相机任务')).toBeTruthy();
});

test('英文空态和空值可读，不输出中文假数据', async () => {
  setup((_request, url) => url.pathname === '/api/v1/runs' ? Response.json(page([])) : undefined, {}, 'en');
  expect(await screen.findByText('No run logs yet')).toBeTruthy();
  expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('Run logs');
});
