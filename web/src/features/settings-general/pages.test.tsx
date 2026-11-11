import { describe, expect, spyOn, test } from 'bun:test';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from '@tanstack/react-router';
import { QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { createApiClient, type ApiFetch } from '../../api/client';
import { ApiProvider } from '../../api/hooks';
import { createAppQueryClient } from '../../app/query-client';
import { ThemeProvider } from '../../app/theme';
import { UnsavedChangesProvider } from '../../app/unsaved-changes';
import { AuthenticationContext } from '../../app/auth-state';
import { apiKey } from '../../api/keys';
import { createI18n, I18nProvider } from '../../i18n';
import type { components } from '../../api/schema.gen';
import { GeneralPage, LimitsPage } from './index';
import { definitions, snapshot } from './fixtures.test';
import ja from '../../../../internal/locales/ja.json';

function pageSnapshot() { const value = snapshot(); value.values['general.language'] = 'zh-CN'; value.sources['general.language'] = 'stored'; return value; }

function json(value: unknown, status = 200) { return new Response(JSON.stringify(value), { status, headers: { 'content-type': 'application/json' } }); }
function failure(code: string, fields: components['schemas']['FieldIssue'][] = [], status = 422) { return json({ error: { code, args: {} }, fields, request_id: 'local-request' }, status); }

function mount(children: ReactNode, fetch: ApiFetch) {
  const queryClient = createAppQueryClient();
  const api = createApiClient({ fetch });
  const root = createRootRoute({ component: () => <UnsavedChangesProvider><Outlet /></UnsavedChangesProvider> });
  const index = createRoute({ getParentRoute: () => root, path: '/', component: () => children });
  const target = createRoute({ getParentRoute: () => root, path: '/settings/limits', component: () => <h1>目标页</h1> });
  const router = createRouter({ routeTree: root.addChildren([index, target]), history: createMemoryHistory({ initialEntries: ['/'] }) });
  const i18n = createI18n('zh-CN');
  const content = (authenticated: boolean) => <I18nProvider instance={i18n}><AuthenticationContext.Provider value={{ authenticated, user: undefined, complete: () => {}, refresh: () => {} }}><ThemeProvider><QueryClientProvider client={queryClient}><ApiProvider api={api}><RouterProvider router={router} /></ApiProvider></QueryClientProvider></ThemeProvider></AuthenticationContext.Provider></I18nProvider>;
  const view = render(content(true));
  return { ...view, queryClient, api, router, setAuthenticated: (authenticated: boolean) => view.rerender(content(authenticated)) };
}

function fixtureFetch(handle?: (request: Request) => Response | Promise<Response | undefined> | undefined): ApiFetch {
  return async (request) => {
    const result = await handle?.(request);
    if (result !== undefined) return result;
    const path = new URL(request.url).pathname;
    if (path === '/api/v1/settings') return json(pageSnapshot());
    if (path === '/api/v1/settings/definitions') return json(definitions);
    if (path === '/api/v1/blocked-sellers') return json({ items: [], next_cursor: null, total: 0 });
    throw new Error(`未声明的本地请求 ${request.method} ${path}`);
  };
}

async function generalReady() { await screen.findByRole('textbox', { name: '时区' }); }

describe('真实设置协议的本地页面回归', () => {
  test('初次加载失败保留请求编号，只有明确重试才再次读取', async () => {
    let reads = 0;
    mount(<GeneralPage />, fixtureFetch((request) => {
      if (new URL(request.url).pathname !== '/api/v1/settings') return undefined;
      reads += 1;
      return reads === 1 ? failure('http.unavailable', [], 503) : json(pageSnapshot());
    }));
    await screen.findByText('请求编号：local-request');
    expect(reads).toBe(1);
    expect(screen.queryByRole('textbox', { name: '时区' })).toBeNull();
    await userEvent.setup().click(screen.getByRole('button', { name: '重试' }));
    await generalReady();
    expect(reads).toBe(2);
  });

  test('422整批失败保留草稿，修正后只提交改动键', async () => {
    const sent: unknown[] = [];
    let current = pageSnapshot();
    mount(<GeneralPage />, fixtureFetch(async (request) => {
      if (new URL(request.url).pathname === '/api/v1/settings' && request.method === 'PATCH') {
        const body: components['schemas']['SettingsPatch'] = await request.json();
        sent.push(body);
        if (sent.length === 1) return failure('settings.invalid', [{ key: 'general.timezone', reason: { code: 'settings.invalid_timezone', args: {} } }]);
        current = { ...current, values: { ...current.values, ...body.values } };
        return json(current);
      }
      return undefined;
    }));
    await generalReady();
    fireEvent.change(screen.getByRole('textbox', { name: '时区' }), { target: { value: 'Not/A_Zone' } });
    fireEvent.change(screen.getByRole('textbox', { name: '外部访问地址' }), { target: { value: 'https://local.test/kakatta' } });
    await userEvent.setup().click(screen.getByRole('button', { name: '保存设置' }));
    await screen.findAllByText('请选择有效的明确时区。');
    expect((screen.getByRole('textbox', { name: '外部访问地址' }) as HTMLInputElement).value).toBe('https://local.test/kakatta');
    expect(current.values['general.public_url']).toBe('');
    fireEvent.change(screen.getByRole('textbox', { name: '时区' }), { target: { value: 'Europe/London' } });
    await userEvent.setup().click(screen.getByRole('button', { name: '保存设置' }));
    await screen.findByText('设置已保存。');
    expect(sent[1]).toEqual({ values: { 'general.timezone': 'Europe/London', 'general.public_url': 'https://local.test/kakatta' } });
  });

  test.each(['en', 'ja'] as const)('保存成功后统一切换%s和外观，只发送这两项', async (language) => {
    let sent: components['schemas']['SettingsPatch'] | undefined;
    mount(<GeneralPage />, fixtureFetch(async (request) => {
      if (request.method !== 'PATCH') return undefined;
      sent = await request.json();
      const current = pageSnapshot();
      if (sent) current.values = { ...current.values, ...sent.values };
      return json(current);
    }));
    await generalReady();
    const user = userEvent.setup();
    if (language === 'ja') expect(screen.getByRole('option', { name: '日本語' }).getAttribute('value')).toBe('ja');
    await user.selectOptions(screen.getByRole('combobox', { name: '语言' }), language);
    await user.selectOptions(screen.getByRole('combobox', { name: '外观' }), 'dark');
    await user.click(screen.getByRole('button', { name: '保存设置' }));
    await screen.findByText(language === 'ja' ? (ja as Record<string, string>)['web.settings.saved']! : 'Settings saved.');
    expect(document.documentElement.lang).toBe(language);
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark');
    expect(sent).toEqual({ values: { 'general.language': language, 'general.appearance': 'dark' } });
  });

  test('推荐外和区间小数按原值发送，26项含唯一时段入口', async () => {
    let sent: components['schemas']['SettingsPatch'] | undefined;
    mount(<LimitsPage />, fixtureFetch(async (request) => {
      if (request.method !== 'PATCH') return undefined;
      sent = await request.json();
      const current = pageSnapshot();
      if (sent) current.values = { ...current.values, ...sent.values };
      current.warnings = [{ key: 'crawl.task_interval_minutes', reason: { code: 'settings.outside_recommendation', args: { min: 30, max: 120 } } }];
      return json(current);
    }));
    await screen.findByRole('textbox', { name: '新任务运行间隔 (分钟)' });
    fireEvent.change(screen.getByRole('textbox', { name: '新任务运行间隔 (分钟)' }), { target: { value: '1' } });
    fireEvent.change(screen.getByRole('textbox', { name: '详情页停留下限（秒）' }), { target: { value: '0.5' } });
    fireEvent.change(screen.getByRole('textbox', { name: '详情页停留上限（秒）' }), { target: { value: '1.75' } });
    expect(screen.getAllByText('超出推荐范围，请查看此项的风险说明；仍可保存。').length).toBeGreaterThan(0);
    expect(screen.queryByRole('switch', { name: '每天不运行的时段' })).toBeNull();
    expect(screen.getByRole('link', { name: '在通用设置中修改' }).getAttribute('href')).toContain('/settings/general');
    await userEvent.setup().click(screen.getByRole('button', { name: '保存设置' }));
    await screen.findByText('设置已保存。');
    expect(sent).toEqual({ values: { 'crawl.task_interval_minutes': 1, 'crawl.detail_stay_seconds': { min: 0.5, max: 1.75 } } });
  });

  test('停止等待保存会取消请求，保留输入并禁止自动重送', async () => {
    let calls = 0;
    let aborted = false;
    mount(<GeneralPage />, fixtureFetch((request) => {
      if (request.method !== 'PATCH') return undefined;
      calls += 1;
      return new Promise<Response>((_resolve, reject) => request.signal.addEventListener('abort', () => { aborted = true; reject(new DOMException('cancelled', 'AbortError')); }, { once: true }));
    }));
    await generalReady();
    fireEvent.change(screen.getByRole('textbox', { name: '外部访问地址' }), { target: { value: 'https://local.test/preserved' } });
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: '保存设置' }));
    await user.click(await screen.findByRole('button', { name: '停止等待' }));
    await screen.findByText('尚未确认保存结果，输入已保留。请重新读取设置后核对，不会自动重送。');
    expect(aborted).toBe(true);
    expect(calls).toBe(1);
    expect((screen.getByRole('textbox', { name: '外部访问地址' }) as HTMLInputElement).value).toBe('https://local.test/preserved');
    expect((screen.getByRole('button', { name: '保存设置' }) as HTMLButtonElement).disabled).toBe(true);
  });

  test('清业务必须输入中文确认词，保留说明可见，文件警告不冒充全部成功', async () => {
    let requestBody: unknown;
    mount(<GeneralPage />, fixtureFetch(async (request) => {
      if (new URL(request.url).pathname !== '/api/v1/maintenance/clear-data') return undefined;
      requestBody = await request.json();
      return json({ deleted: { tasks: 2, listings: 3 }, images_deleted: 1, warnings: [{ code: 'maintenance.image_cleanup_failed', args: {} }] });
    }));
    await generalReady();
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: '清除全部业务数据' }));
    const dialog = screen.getByRole('dialog', { name: '清除全部业务数据' });
    expect(within(dialog).getByText(/访问额度、模型调用记录和账号冷却会保留/)).toBeTruthy();
    const confirm = within(dialog).getByRole('button', { name: '清除' });
    expect((confirm as HTMLButtonElement).disabled).toBe(true);
    await user.type(within(dialog).getByRole('textbox', { name: '请输入「清除」确认' }), 'CLEAR');
    expect((confirm as HTMLButtonElement).disabled).toBe(true);
    await user.clear(within(dialog).getByRole('textbox', { name: '请输入「清除」确认' }));
    await user.type(within(dialog).getByRole('textbox', { name: '请输入「清除」确认' }), '清除');
    await user.click(confirm);
    await screen.findByText('清理已返回，但仍有事项需要处理');
    expect(requestBody).toEqual({ confirmation: '清除' });
    expect(screen.queryByText('业务数据已清除，设置和账号保护记录已保留。')).toBeNull();
    expect(screen.getByText('删除了 5 条记录')).toBeTruthy();
  });

  test.each([null, ''])('屏蔽卖家昵称 %j 按游标翻页，解除失败保留列表和请求编号', async (nick) => {
    const listRequests: URL[] = [];
    let attempts = 0;
    let removed = false;
    mount(<GeneralPage />, fixtureFetch((request) => {
      const url = new URL(request.url);
      if (url.pathname === '/api/v1/blocked-sellers') {
        listRequests.push(url);
        return json(url.searchParams.has('cursor') ? { items: removed ? [] : [{ seller_id: 'seller-last', nick, created_at: null }], next_cursor: null, total: removed ? 50 : 51 } : { items: Array.from({ length: 50 }, (_, index) => ({ seller_id: `seller-${index}`, nick: `本地卖家${index}`, created_at: null })), next_cursor: 'opaque-next', total: removed ? 50 : 51 });
      }
      if (url.pathname === '/api/v1/blocked-sellers/seller-last' && request.method === 'DELETE') {
        attempts += 1;
        if (attempts === 1) return failure('service.unavailable', [], 409);
        removed = true;
        return new Response(null, { status: 204 });
      }
      return undefined;
    }));
    await generalReady();
    const region = await screen.findByRole('region', { name: '屏蔽的卖家' });
    const user = userEvent.setup();
    await user.click(within(region).getByRole('button', { name: '下一页' }));
    await within(region).findByText('未取得卖家昵称');
    expect(listRequests.at(-1)?.searchParams.get('cursor')).toBe('opaque-next');
    expect(listRequests.at(-1)?.searchParams.get('limit')).toBe('50');
    await user.click(within(region).getByRole('button', { name: '解除屏蔽 seller-last' }));
    await within(region).findByText('请求编号：local-request');
    expect(within(region).getByText('seller-last')).toBeTruthy();
    expect(attempts).toBe(1);
    await user.click(within(region).getByRole('button', { name: '解除屏蔽 seller-last' }));
    await within(region).findByText('已解除屏蔽，旧商品不会变成新发现。');
    expect(attempts).toBe(2);
  });

  test('离开未完成读取会传递AbortSignal', async () => {
    let reads = 0;
    let cancelled = 0;
    const view = mount(<GeneralPage />, async (request) => {
      reads += 1;
      return new Promise<Response>((_resolve, reject) => request.signal.addEventListener('abort', () => { cancelled += 1; reject(new DOMException('cancelled', 'AbortError')); }, { once: true }));
    });
    await waitFor(() => expect(reads).toBe(2));
    view.unmount();
    await waitFor(() => expect(cancelled).toBe(2));
  });
});


const lateActions = [
  { kind: 'settings', name: '保存设置', method: 'PATCH', path: '/api/v1/settings' },
  { kind: 'images', name: '清除图片缓存', method: 'POST', path: '/api/v1/maintenance/clear-image-cache' },
  { kind: 'data', name: '清除全部业务数据', method: 'POST', path: '/api/v1/maintenance/clear-data' },
  { kind: 'unblock', name: '解除屏蔽', method: 'DELETE', path: '/api/v1/blocked-sellers/late-seller' },
] as const;

for (const action of lateActions) {
  for (const cancellation of ['manual', 'authentication'] as const) {
    test(`${action.name}在${cancellation === 'manual' ? '停止等待' : '认证失效'}后忽略迟到成功，不改草稿或缓存`, async () => {
      let respond: ((response: Response) => void) | undefined;
      let signal: AbortSignal | undefined;
      const page = mount(<GeneralPage />, fixtureFetch((request) => {
        const path = new URL(request.url).pathname;
        if (request.method === action.method && path === action.path) {
          signal = request.signal;
          return new Promise<Response>((resolve) => { respond = resolve; });
        }
        if (path === '/api/v1/blocked-sellers') return json({ items: [{ seller_id: 'late-seller', nick: '等待解除', created_at: null }], next_cursor: null, total: 1 });
        if (path === '/api/v1/me') return failure('auth.unauthenticated', [], 401);
        return undefined;
      }));
      await generalReady();
      fireEvent.change(screen.getByRole('textbox', { name: '外部访问地址' }), { target: { value: 'https://local.test/kept' } });
      const user = userEvent.setup();
      if (action.kind === 'settings') await user.click(screen.getByRole('button', { name: '保存设置' }));
      if (action.kind === 'images') await user.click(screen.getByRole('button', { name: '清除图片缓存' }));
      if (action.kind === 'data') {
        await user.click(screen.getByRole('button', { name: '清除全部业务数据' }));
        const dialog = screen.getByRole('dialog', { name: '清除全部业务数据' });
        await user.type(within(dialog).getByRole('textbox', { name: '请输入「清除」确认' }), '清除');
        await user.click(within(dialog).getByRole('button', { name: '清除' }));
      }
      if (action.kind === 'unblock') await user.click(screen.getByRole('button', { name: '解除屏蔽 等待解除' }));
      await waitFor(() => expect(signal).toBeDefined());
      const invalidate = spyOn(page.queryClient, 'invalidateQueries');
      try {
        if (cancellation === 'manual') await user.click(screen.getByRole('button', { name: '停止等待' }));
        else await act(async () => { await page.api.GET('/api/v1/me').catch(() => {}); });
        expect(signal?.aborted).toBe(true);
        const value = pageSnapshot();
        value.values['general.public_url'] = 'https://remote.test/late';
        value.values['general.language'] = 'en';
        value.values['general.appearance'] = 'dark';
        await act(async () => {
          respond?.(action.kind === 'settings' ? json(value) : action.kind === 'unblock' ? new Response(null, { status: 204 }) : json({ deleted: { tasks: 9 }, images_deleted: 4, warnings: [] }));
          await new Promise<void>((resolve) => setTimeout(resolve, 0));
        });
        expect((screen.getByRole('textbox', { name: '外部访问地址', hidden: true }) as HTMLInputElement).value).toBe('https://local.test/kept');
        expect(page.queryClient.getQueryData<components['schemas']['SettingsSnapshot']>(apiKey('settings'))?.values['general.public_url']).toBe('');
        expect(invalidate).not.toHaveBeenCalled();
        expect(screen.queryByText('设置已保存。')).toBeNull();
        expect(screen.queryByText('图片缓存已清除。')).toBeNull();
        expect(screen.queryByText('业务数据已清除，设置和账号保护记录已保留。')).toBeNull();
        expect(screen.queryByText('已解除屏蔽，旧商品不会变成新发现。')).toBeNull();
      } finally { invalidate.mockRestore(); page.unmount(); }
    });
  }
}

test('设置草稿交给统一离开确认，取消保留输入，确认只出现一次', async () => {
  const page = mount(<GeneralPage />, fixtureFetch());
  await generalReady();
  const user = userEvent.setup();
  const input = screen.getByRole<HTMLInputElement>('textbox', { name: '外部访问地址' });
  fireEvent.change(input, { target: { value: 'https://local.test/stay' } });
  await user.click(screen.getByRole('link', { name: '抓取与限制' }));
  const dialog = await screen.findByRole('alertdialog', { name: '放弃未保存的修改？' });
  expect(screen.getAllByRole('alertdialog')).toHaveLength(1);
  await user.click(within(dialog).getByRole('button', { name: '取消' }));
  expect(input.value).toBe('https://local.test/stay');
  expect(page.router.state.location.pathname).toBe('/');
  await user.click(screen.getByRole('link', { name: '抓取与限制' }));
  await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: '放弃修改并离开' }));
  await screen.findByRole('heading', { name: '目标页' });
  expect(screen.queryByRole('alertdialog')).toBeNull();
});


test('认证上下文失效立即取消保存，恢复前保留并禁用普通草稿', async () => {
  let respond: ((response: Response) => void) | undefined;
  let signal: AbortSignal | undefined;
  const page = mount(<GeneralPage />, fixtureFetch((request) => {
    if (request.method !== 'PATCH') return undefined;
    signal = request.signal;
    return new Promise<Response>((resolve) => { respond = resolve; });
  }));
  await generalReady();
  const input = screen.getByRole<HTMLInputElement>('textbox', { name: '外部访问地址' });
  fireEvent.change(input, { target: { value: 'https://local.test/auth-kept' } });
  await userEvent.setup().click(screen.getByRole('button', { name: '保存设置' }));
  await waitFor(() => expect(signal).toBeDefined());
  page.setAuthenticated(false);
  expect(signal?.aborted).toBe(true);
  expect(input.disabled).toBe(true);
  const value = pageSnapshot();
  value.values['general.public_url'] = 'https://remote.test/stale';
  await act(async () => { respond?.(json(value)); await new Promise<void>((resolve) => setTimeout(resolve, 0)); });
  expect(input.value).toBe('https://local.test/auth-kept');
  expect(screen.queryByText('设置已保存。')).toBeNull();
  expect(page.queryClient.getQueryData<components['schemas']['SettingsSnapshot']>(apiKey('settings'))?.values['general.public_url']).toBe('');
});
