import { afterEach, describe, expect, mock, spyOn, test } from 'bun:test';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClientProvider, type QueryClient } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from '@tanstack/react-router';
import { useState, type ReactNode } from 'react';
import { createApiClient } from '../../api/client';
import { ApiProvider } from '../../api/hooks';
import type { Schema } from '../../api/types';
import { createAppQueryClient } from '../../app/query-client';
import { AuthenticationContext } from '../../app/auth-state';
import { UnsavedChangesProvider } from '../../app/unsaved-changes';
import { createI18n, I18nProvider, type Locale } from '../../i18n';
import { formatDateTime } from '../../i18n/format';
import { TaskEditor, TaskSaveFeedback, TasksPage } from './index';

const clients: QueryClient[] = [];
afterEach(() => { for (const client of clients.splice(0)) client.clear(); });
const json = (value: unknown, status = 200) => Response.json(value, { status });
const failure = (status: number, fields: Schema<'FieldIssue'>[] = []) => json({ error: { code: 'future.task_error', args: {} }, fields, request_id: 'task-request-17' }, status);
function fields(patch: Partial<Schema<'TaskFields'>> = {}): Schema<'TaskFields'> {
  return { name: '', keyword: '', enabled: true, account_id: null, account_pinned: false, min_price: null, max_price: null, personal_only: true, free_shipping_only: false, publish_window: 'any', region_province: null, region_city: null, region_district: null, max_pages: 1, interval_minutes: 30, crawl_overrides: {}, judge_mode: 'keyword', requirement: '', profile: {}, budget_min: null, budget_max: null, match_keywords: [], exclude_keywords: [], analyze_on_first_run: true, notify_policy: 'recommended', notify_price_drop: true, channel_ids: [], ...patch };
}
function task(patch: Partial<Schema<'Task'>> = {}): Schema<'Task'> {
  return { ...fields({ name: '书房相机', keyword: '相机' }), id: 17, search_generation: 1, baseline_pending: true, sort_index: 0, created_at: 1794355200000, updated_at: 1794355200000, last_run_at: null, last_success_at: null, next_run_at: 1794355200000, last_run_outcome: null, last_error: null, consecutive_failures: 0, failure_paused_until: null, no_good_hint_dismissed_at: null, ...patch };
}
function view(patch: Partial<Schema<'TaskView'>> = {}): Schema<'TaskView'> {
  return { ...task(), runtime: { task_id: 17, phase: 'idle', run_id: null, account_id: null, queued_at: null, started_at: null, blocked: null }, finding_counts: { total: 0 }, no_good_hint: null, ...patch };
}
function saved(value: Schema<'Task'> = task(), effects: Partial<Schema<'TaskSaveEffects'>> = {}): Schema<'TaskSaveResult'> {
  return { task: value, warnings: [], effects: { search_reset: false, generation: value.search_generation, rescored_count: 0, needs_reanalysis_count: 0, needs_reanalysis_ids: [], title_rejected_count: 0, title_rejected_ids: [], added_exclude_words: [], existing_exclude_match_count: 0, ...effects } };
}
const profile: Schema<'AnalysisProfileFields'> = { summary: '相机，国行', specs: [], musts: [{ id: 'm7', text: '国行', rev: 3, source: 'user', edited: true }], bonuses: [], risks: [{ id: 'id_lock', attitude: 'confirm', source: 'user', edited: false, rev: 1 }, { id: 'off_platform', attitude: 'reject', source: 'system', edited: false, rev: 1 }], allowed_kinds: [], next_no: 9 };
const dictionary: Schema<'AnalysisRiskDefinition'>[] = [{ id: 'id_lock', name: 'ID 锁', category: '锁和账号', default_attitude: 'confirm', hint: '账号没退出', question: '账号退出了吗？' }, { id: 'off_platform', name: '站外交易', category: '交易方式', default_attitude: 'reject', hint: '要求站外付款', question: '' }];
const provider: Schema<'ProviderView'> = { id: 4, name: '本地假模型', base_url: 'http://fake-model.invalid/v1', vision_model: 'fake-vision', text_model: null, api_key_set: true, enabled: true, error_at: null, error_state: null, preset: null, sort_index: 0 };

function AuthenticationHarness({ children }: { children: ReactNode }) {
  const [authenticated, setAuthenticated] = useState(true);
  return <AuthenticationContext.Provider value={{ authenticated, user: undefined, complete() {}, refresh() {} }}><button onClick={() => setAuthenticated(!authenticated)}>{authenticated ? '使认证失效' : '恢复认证'}</button>{children}</AuthenticationContext.Provider>;
}

async function mount(children: ReactNode, handle: (request: Request) => Response | Promise<Response | undefined> | undefined = () => undefined, options: { defaults?: Schema<'TaskFields'>; providers?: Schema<'ProviderView'>[]; locale?: Locale; protectUnsaved?: boolean } = {}) {
  const requests: Request[] = [];
  const client = createAppQueryClient(); clients.push(client);
  const api = createApiClient({ fetch: async (request) => {
    requests.push(request.clone());
    const handled = await handle(request);
    if (handled !== undefined) return handled;
    const url = new URL(request.url);
    if (url.pathname === '/api/v1/tasks/defaults') return json(options.defaults ?? fields());
    if (url.pathname === '/api/v1/risk-dictionary') return json(dictionary);
    if (url.pathname === '/api/v1/settings') return json({ values: { 'general.timezone': 'Asia/Shanghai' }, sources: {}, warnings: [] });
    if (url.pathname === '/api/v1/settings/definitions') return json([]);
    if (url.pathname === '/api/v1/accounts') return json({ items: [], next_cursor: null, total: 0 });
    if (url.pathname === '/api/v1/channels') return json([]);
    if (url.pathname === '/api/v1/ai/providers') return json(options.providers ?? []);
    if (url.pathname === '/api/v1/regions') {
      const parent = url.searchParams.get('parent');
      return json(parent === 'zj' ? [{ id: 'hz', name: '杭州', level: 'city', parent_id: 'zj' }] : parent === 'hz' ? [{ id: 'xh', name: '西湖', level: 'district', parent_id: 'hz' }] : parent ? [] : [{ id: 'zj', name: '浙江', level: 'province', parent_id: null }]);
    }
    if (url.pathname === '/api/v1/tasks' && request.method === 'GET') return json({ items: [], total: 0, next_cursor: null });
    throw new Error(`非预期本地请求 ${request.method} ${url.pathname}`);
  } });
  const root = createRootRoute({ component: () => options.protectUnsaved ? <UnsavedChangesProvider><Outlet /></UnsavedChangesProvider> : <Outlet /> });
  const index = createRoute({ getParentRoute: () => root, path: '/', component: () => <>{children}</> });
  const target = createRoute({ getParentRoute: () => root, path: '/overview', component: () => <h1>目标页</h1> });
  const router = createRouter({ routeTree: root.addChildren([index, target]), history: createMemoryHistory({ initialEntries: ['/'] }) });
  await router.load();
  const rendered = render(<I18nProvider instance={createI18n(options.locale ?? 'zh-CN')}><QueryClientProvider client={client}><ApiProvider api={api}><RouterProvider router={router} /></ApiProvider></QueryClientProvider></I18nProvider>);
  await act(async () => { await new Promise<void>((resolve) => setTimeout(resolve, 0)); });
  return { ...rendered, requests, client, api, router };
}

describe('完整任务编辑器', () => {
  test('读取真实默认，全部搜索筛选与元分转换按Create发送，未显式名称不写入', async () => {
    const onSaved = mock(() => {});
    const mounted = await mount(<TaskEditor onSaved={onSaved} />, (request) => request.method === 'POST' && new URL(request.url).pathname === '/api/v1/tasks' ? json(saved(), 201) : undefined);
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText('搜索关键词'), '  相机  ');
    await user.type(screen.getByLabelText('最低价格（元）'), '0');
    await user.type(screen.getByLabelText('最高价格（元）'), '1999.01');
    await user.click(screen.getByLabelText('只看个人闲置'));
    await user.click(screen.getByLabelText('只看包邮'));
    await user.selectOptions(screen.getByLabelText('发布时间'), 'day7');
    await user.selectOptions(screen.getByLabelText('省或地区'), '浙江');
    await screen.findByRole('option', { name: '杭州' });
    await user.selectOptions(screen.getByLabelText('市'), '杭州');
    await screen.findByRole('option', { name: '西湖' });
    await user.selectOptions(screen.getByLabelText('区'), '西湖');
    await user.clear(screen.getByLabelText('每次翻几页')); await user.type(screen.getByLabelText('每次翻几页'), '4');
    await user.clear(screen.getByLabelText('运行间隔（分钟）')); await user.type(screen.getByLabelText('运行间隔（分钟）'), '5');
    await user.type(screen.getByLabelText('排除词'), 'ＮＶＭｅ，nvme\n维修机');
    await user.selectOptions(screen.getByLabelText('推送哪些商品'), 'all_new');
    await user.click(screen.getByRole('button', { name: '保存任务' }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    const request = mounted.requests.find((item) => item.method === 'POST' && new URL(item.url).pathname === '/api/v1/tasks');
    expect(request).toBeDefined();
    const body: unknown = await request?.json();
    expect(body).toMatchObject({ keyword: '相机', min_price: 0, max_price: 199901, personal_only: false, free_shipping_only: true, publish_window: 'day7', region_province: '浙江', region_city: '杭州', region_district: '西湖', max_pages: 4, interval_minutes: 5, budget_min: null, budget_max: null, notify_policy: 'all_new', channel_ids: [], exclude_keywords: ['ＮＶＭｅ', '维修机'] });
    expect(typeof body === 'object' && body !== null && Object.hasOwn(body, 'name')).toBe(false);
  });

  test('422保留完整输入，字段错误安全翻译并聚焦，不自动重试', async () => {
    let writes = 0;
    await mount(<TaskEditor initialTask={task()} onSaved={() => {}} />, (request) => {
      if (request.method === 'PATCH') { writes += 1; return failure(422, [{ key: 'keyword', reason: { code: 'tasks.text_length', args: { min: 1, max: 50 } } }]); }
      return undefined;
    });
    const user = userEvent.setup(), keyword = await screen.findByLabelText('搜索关键词');
    await user.clear(keyword); await user.type(keyword, '保留这个搜索词');
    await user.click(screen.getByRole('button', { name: '保存任务' }));
    await waitFor(() => expect(keyword.getAttribute('aria-invalid')).toBe('true'));
    expect((keyword as HTMLInputElement).value).toBe('保留这个搜索词');
    await waitFor(() => expect(document.activeElement).toBe(keyword));
    expect(screen.queryByText('future.task_error')).toBeNull();
    expect(screen.getByText(/task-request-17/)).toBeTruthy();
    expect(writes).toBe(1);
  });

  test('AI切关键词先确认，取消不清手改，确认后profile为空且策略切到合法默认', async () => {
    let writes = 0, body: unknown;
    const aiTask = task({ judge_mode: 'ai', notify_policy: 'good', profile });
    await mount(<TaskEditor initialTask={aiTask} onSaved={() => {}} />, (request) => {
      if (request.method === 'PATCH') { writes += 1; return request.clone().json().then((value: unknown) => { body = value; return json(saved(task({ judge_mode: 'keyword', profile: {}, notify_policy: 'recommended' }))); }); }
      return undefined;
    });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('radio', { name: '关键词判断' }));
    await user.click(screen.getByRole('button', { name: '保存任务' }));
    const confirm = await screen.findByRole('alertdialog', { name: '清空需求清单并保存？' });
    expect(writes).toBe(0);
    await user.click(within(confirm).getByRole('button', { name: '取消' }));
    await user.click(screen.getByRole('radio', { name: 'AI 判断' }));
    expect((screen.getByLabelText('必须满足 1') as HTMLInputElement).value).toBe('国行');
    await user.click(screen.getByRole('radio', { name: '关键词判断' }));
    await user.click(screen.getByRole('button', { name: '保存任务' }));
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: '保存任务' }));
    await waitFor(() => expect(writes).toBe(1));
    expect(body).toMatchObject({ judge_mode: 'keyword', profile: {}, notify_policy: 'recommended' });
  });

  test('四档风险可改、通用项可删，自写项只送空编号由后台分配', async () => {
    let body: unknown;
    await mount(<TaskEditor initialTask={task({ judge_mode: 'ai', notify_policy: 'good', profile })} onSaved={() => {}} />, (request) => {
      if (request.method === 'PATCH') return request.clone().json().then((value: unknown) => { body = value; return json(saved()); });
      return undefined;
    });
    const user = userEvent.setup();
    const idLock = await screen.findByRole('radiogroup', { name: 'ID 锁的态度' });
    await user.click(within(idLock).getByRole('radio', { name: /介意/ }));
    await user.click(screen.getByRole('button', { name: '删除站外交易' }));
    await user.click(screen.getByRole('button', { name: '自己写一条' }));
    await user.type(screen.getByLabelText('检查项名称'), '屏幕压伤');
    await user.click(screen.getByRole('button', { name: '保存任务' }));
    await waitFor(() => expect(body).toBeDefined());
    expect(body).toMatchObject({ profile: { risks: [{ id: 'id_lock', rev: 1, attitude: 'mind', edited: true }, { id: '', rev: 1, name: '屏幕压伤', attitude: 'mind', source: 'user', edited: true }] } });
  });

  test('无AI时可手动编辑且不显示生成按钮', async () => {
    await mount(<TaskEditor initialTask={task({ judge_mode: 'ai', notify_policy: 'good', profile })} onSaved={() => {}} />);
    await screen.findByText('没有配置 AI，你仍可以自己添加规格、条件和检查项。');
    expect(screen.queryByRole('button', { name: '生成清单' })).toBeNull();
    expect((screen.getByRole('button', { name: '自己写一条' }) as HTMLButtonElement).disabled).toBe(false);
  });

  test('任务覆盖展示单位、默认和风险，越推荐仍可保存，跟随全局不写覆盖', async () => {
    let body: unknown;
    const definition: Schema<'SettingDefinition'> = { key: 'crawl.page_wait_seconds', group: 'crawl', kind: 'range', default: { min: 10, max: 15 }, unit: 'seconds', options: [], recommendation: { min: 10, max: null, text: { code: 'settings.crawl.page_wait_seconds.recommendation', args: {} } }, label: { code: 'settings.crawl.page_wait_seconds.label', args: {} }, why: { code: 'settings.crawl.page_wait_seconds.why', args: {} }, risk: { code: 'settings.crawl.page_wait_seconds.risk', args: {} }, task_override: true };
    await mount(<TaskEditor initialTask={task()} onSaved={() => {}} />, (request) => {
      const path = new URL(request.url).pathname;
      if (path === '/api/v1/settings/definitions') return json([definition, { ...definition, key: 'crawl.account_hourly_visits', task_override: false }]);
      if (path === '/api/v1/settings') return json({ values: { 'general.timezone': 'Asia/Shanghai', 'crawl.page_wait_seconds': { min: 20, max: 25 } }, sources: {}, warnings: [] });
      if (request.method === 'PATCH') return request.clone().json().then((value: unknown) => { body = value; return json(saved()); });
      return undefined;
    });
    const user = userEvent.setup();
    const minimum = await screen.findByRole('textbox', { name: /下限（秒）/ });
    expect((minimum as HTMLInputElement).value).toBe('20');
    expect((minimum as HTMLInputElement).disabled).toBe(true);
    expect(screen.getByText('默认值')).toBeTruthy();
    expect(screen.getByText('10–15 秒')).toBeTruthy();
    await user.click(screen.getByLabelText('跟随全局设置'));
    expect(minimum.getAttribute('inputmode')).toBe('decimal');
    await user.clear(minimum); await user.type(minimum, '0.5');
    const maximum = screen.getByRole('textbox', { name: /上限（秒）/ });
    expect(maximum.getAttribute('inputmode')).toBe('decimal');
    await user.clear(maximum); await user.type(maximum, '1.75');
    await screen.findByText('超出推荐范围，请查看此项的风险说明；仍可保存。');
    await user.click(screen.getByRole('button', { name: '保存任务' }));
    await waitFor(() => expect(body).toMatchObject({ crawl_overrides: { page_wait_seconds: { min: 0.5, max: 1.75 } } }));
    expect(screen.getAllByLabelText('跟随全局设置')).toHaveLength(1);
  });

  test('新建成功后仍留在嵌入编辑器时，继续保存改为PATCH，不重复创建', async () => {
    const calls: string[] = [];
    await mount(<TaskEditor onSaved={() => {}} />, (request) => {
      const path = new URL(request.url).pathname;
      if (path === '/api/v1/tasks' && request.method === 'POST' || path === '/api/v1/tasks/17' && request.method === 'PATCH') { calls.push(request.method); return json(saved(), request.method === 'POST' ? 201 : 200); }
      return undefined;
    });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText('搜索关键词'), '相机');
    await user.click(screen.getByRole('button', { name: '保存任务' }));
    await waitFor(() => expect(calls).toEqual(['POST']));
    const name = screen.getByLabelText('任务名称');
    await user.clear(name); await user.type(name, '改过的名字');
    await user.click(screen.getByRole('button', { name: '保存任务' }));
    await waitFor(() => expect(calls).toEqual(['POST', 'PATCH']));
  });

  test('任务已被删除时保留草稿，只有明确转为复制并保存才Create', async () => {
    const calls: string[] = [];
    await mount(<TaskEditor initialTask={task()} onSaved={() => {}} />, (request) => {
      if (request.method === 'PATCH') { calls.push('PATCH'); return failure(404); }
      if (request.method === 'POST') { calls.push('POST'); return json(saved(task({ id: 18 })), 201); }
      return undefined;
    });
    const user = userEvent.setup(), keyword = await screen.findByLabelText('搜索关键词');
    await user.clear(keyword); await user.type(keyword, '保留的新关键词');
    await user.click(screen.getByRole('button', { name: '保存任务' }));
    await user.click(await screen.findByRole('button', { name: '复制任务' }));
    expect(calls).toEqual(['PATCH']);
    expect((screen.getByLabelText('搜索关键词') as HTMLInputElement).value).toBe('保留的新关键词');
    await user.click(screen.getByRole('button', { name: '保存任务' }));
    await waitFor(() => expect(calls).toEqual(['PATCH', 'POST']));
  });

  test('认证失效取消保存并保留草稿，恢复后不重发；旧保存晚到不能覆盖新保存或触发回调', async () => {
    const onSaved = mock(() => {}), signals: AbortSignal[] = [];
    let finish = (_response: Response) => {}, writes = 0;
    await mount(<AuthenticationHarness><TaskEditor initialTask={task()} onSaved={onSaved} /></AuthenticationHarness>, (request) => {
      if (request.method === 'PATCH') {
        signals.push(request.signal); writes += 1;
        return writes === 1 ? new Promise<Response>((resolve) => { finish = resolve; }) : json(saved(task({ keyword: '重新登录后的草稿' })));
      }
      return undefined;
    });
    const user = userEvent.setup(), keyword = await screen.findByLabelText('搜索关键词');
    await user.clear(keyword); await user.type(keyword, '失效前的草稿');
    await user.click(screen.getByRole('button', { name: '保存任务' }));
    await waitFor(() => expect(writes).toBe(1));
    await user.click(screen.getByRole('button', { name: '使认证失效' }));
    expect(signals[0]?.aborted).toBe(true);
    expect((keyword as HTMLInputElement).value).toBe('失效前的草稿');
    expect((screen.getByRole('button', { name: '保存任务' }) as HTMLButtonElement).disabled).toBe(true);
    await user.click(screen.getByRole('button', { name: '恢复认证' }));
    expect(writes).toBe(1);
    await user.clear(keyword); await user.type(keyword, '重新登录后的草稿');
    await user.click(screen.getByRole('button', { name: '保存任务' }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    await act(async () => { finish(json(saved(task({ keyword: '失效前的草稿' })))); await new Promise<void>((resolve) => setTimeout(resolve, 0)); });
    expect((keyword as HTMLInputElement).value).toBe('重新登录后的草稿');
    expect(onSaved).toHaveBeenCalledTimes(1);
    expect(writes).toBe(2);
  });
});

describe('真实需求生成', () => {
  test('发当前手改清单和真实需求，合并结果保留手改；建议不自动覆盖关键词、预算和搜索价格', async () => {
    let input: unknown;
    const generated: Schema<'GeneratedDraft'> = { profile: { ...profile, bonuses: [{ id: 'b9', rev: 1, text: '有发票', source: 'ai', edited: false }], next_no: 10 }, warnings: [], suggestions: { keyword: 'iPad mini', exclude_words: ['回收'], budget_min: 200000, budget_max: 300000 } };
    await mount(<TaskEditor initialTask={task({ judge_mode: 'ai', notify_policy: 'good', profile, requirement: '国行，预算两三千', max_price: 250000, exclude_keywords: ['原排除词'] })} onSaved={() => {}} />, (request) => {
      if (new URL(request.url).pathname === '/api/v1/ai/profile') return request.clone().json().then((value: unknown) => { input = value; return json(generated); });
      return undefined;
    }, { providers: [provider] });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '生成清单' }));
    await screen.findByLabelText('加分项 1');
    expect(input).toMatchObject({ task_id: 17, keyword: '相机', requirement: '国行，预算两三千', current: { musts: [{ id: 'm7', rev: 3, text: '国行', source: 'user', edited: true }] } });
    expect((screen.getByLabelText('必须满足 1') as HTMLInputElement).value).toBe('国行');
    expect((screen.getByLabelText('搜索关键词') as HTMLInputElement).value).toBe('相机');
    expect((screen.getByLabelText('预算上限（元）') as HTMLInputElement).value).toBe('');
    expect((screen.getByLabelText('最高价格（元）') as HTMLInputElement).value).toBe('2500');
    const budgetSection = screen.getByRole('region', { name: '预算' });
    expect(within(budgetSection).queryByRole('button', { name: '填入' })).toBeNull();
    const suggestions = screen.getByRole('region', { name: '生成时的建议，点一下才填入' });
    const apply = within(suggestions).getAllByRole('button', { name: '填入' });
    const budget = apply[2]; if (!budget) throw new Error('预算建议按钮缺失');
    await user.click(budget);
    expect((screen.getByLabelText('预算上限（元）') as HTMLInputElement).value).toBe('3000');
    expect((screen.getByLabelText('最高价格（元）') as HTMLInputElement).value).toBe('2500');
    expect(within(budgetSection).getByText(/建议把搜索价格上限设为 ¥3,600.00/)).toBeTruthy();
    await user.click(within(budgetSection).getByRole('button', { name: '填入' }));
    expect((screen.getByLabelText('最高价格（元）') as HTMLInputElement).value).toBe('3600');
    expect((screen.getByLabelText('预算上限（元）') as HTMLInputElement).value).toBe('3000');
    expect((screen.getByLabelText('搜索关键词') as HTMLInputElement).value).toBe('相机');
    const exclusions = apply[1]; if (!exclusions) throw new Error('排除词建议按钮缺失');
    await user.click(exclusions);
    expect((screen.getByLabelText('排除词') as HTMLTextAreaElement).value).toBe('原排除词\n回收');
  });

  test.each(['75059993789504.17', '90071992547409.91', '无效预算', '-1', '1.001'])('预算输入%s不产生无效搜索价建议，也不改原输入或搜索价', async (budget) => {
    await mount(<TaskEditor initialTask={task({ max_price: 250000 })} onSaved={() => {}} />);
    const user = userEvent.setup(), input = await screen.findByLabelText('预算上限（元）');
    await user.type(input, budget);
    expect((input as HTMLInputElement).value).toBe(budget);
    expect((screen.getByLabelText('最高价格（元）') as HTMLInputElement).value).toBe('2500');
    expect(within(screen.getByRole('region', { name: '预算' })).queryByRole('button', { name: '填入' })).toBeNull();
  });

  test('15秒后显示仍生成；取消保留清单，晚到响应不能覆盖，收费动作不自动重试', async () => {
    let finish = (_response: Response) => {};
    let calls = 0;
    const timeout = globalThis.setTimeout;
    const timerDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'setTimeout');
    Object.defineProperty(globalThis, 'setTimeout', { configurable: true, writable: true, value: (...args: Parameters<typeof setTimeout>) => timeout(args[0], args[1] === 15_000 ? 0 : args[1], ...args.slice(2)) });
    try {
      await mount(<TaskEditor initialTask={task({ judge_mode: 'ai', notify_policy: 'good', profile })} onSaved={() => {}} />, (request) => {
        if (new URL(request.url).pathname === '/api/v1/ai/profile') { calls += 1; return new Promise<Response>((resolve) => { finish = resolve; }); }
        return undefined;
      }, { providers: [provider] });
      const user = userEvent.setup();
      await user.click(await screen.findByRole('button', { name: '生成清单' }));
      await screen.findByText('仍在生成…');
      await user.click(screen.getByRole('button', { name: '取消' }));
      await screen.findByText('已取消生成，原清单已保留。');
      finish(json({ profile: { ...profile, musts: [{ id: 'm99', text: '不应出现', rev: 1, source: 'ai', edited: false }] }, suggestions: { keyword: null, exclude_words: [], budget_min: null, budget_max: null }, warnings: [] }));
      await new Promise((resolve) => timeout(resolve, 10));
      expect((screen.getByLabelText('必须满足 1') as HTMLInputElement).value).toBe('国行');
      expect(screen.queryByDisplayValue('不应出现')).toBeNull();
      expect(calls).toBe(1);
    } finally { if (timerDescriptor) Object.defineProperty(globalThis, 'setTimeout', timerDescriptor); }
  });

  test('生成失败保留原清单与需求，显示安全原因和请求编号，不自动重发', async () => {
    let calls = 0;
    await mount(<TaskEditor initialTask={task({ judge_mode: 'ai', notify_policy: 'good', profile, requirement: '国行相机' })} onSaved={() => {}} />, (request) => {
      if (new URL(request.url).pathname === '/api/v1/ai/profile') { calls += 1; return failure(503); }
      return undefined;
    }, { providers: [provider] });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '生成清单' }));
    await screen.findByText(/task-request-17/);
    expect((screen.getByLabelText('我的要求') as HTMLTextAreaElement).value).toBe('国行相机');
    expect((screen.getByLabelText('必须满足 1') as HTMLInputElement).value).toBe('国行');
    expect(screen.queryByText('future.task_error')).toBeNull();
    expect(calls).toBe(1);
  });

  test('认证失效立即取消生成，保留需求；恢复不自动收费，旧响应不能覆盖新一代清单', async () => {
    let finish = (_response: Response) => {}, calls = 0;
    const signals: AbortSignal[] = [];
    const generated = (text: string): Schema<'GeneratedDraft'> => ({ profile: { ...profile, musts: [{ id: 'm7', text, rev: 4, source: 'user', edited: true }] }, suggestions: { keyword: null, exclude_words: [], budget_min: null, budget_max: null }, warnings: [] });
    await mount(<AuthenticationHarness><TaskEditor initialTask={task({ judge_mode: 'ai', notify_policy: 'good', profile, requirement: '保留的需求' })} onSaved={() => {}} /></AuthenticationHarness>, (request) => {
      if (new URL(request.url).pathname === '/api/v1/ai/profile') {
        calls += 1; signals.push(request.signal);
        return calls === 1 ? new Promise<Response>((resolve) => { finish = resolve; }) : json(generated('新一代清单'));
      }
      return undefined;
    }, { providers: [provider] });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '生成清单' }));
    await user.click(screen.getByRole('button', { name: '使认证失效' }));
    expect(signals[0]?.aborted).toBe(true);
    expect((screen.getByLabelText('我的要求') as HTMLTextAreaElement).value).toBe('保留的需求');
    expect((screen.getByLabelText('必须满足 1') as HTMLInputElement).value).toBe('国行');
    expect((screen.getByRole('button', { name: '生成清单' }) as HTMLButtonElement).disabled).toBe(true);
    await user.click(screen.getByRole('button', { name: '恢复认证' }));
    expect(calls).toBe(1);
    await user.click(screen.getByRole('button', { name: '生成清单' }));
    await screen.findByDisplayValue('新一代清单');
    await act(async () => { finish(json(generated('迟到的清单'))); await new Promise<void>((resolve) => setTimeout(resolve, 0)); });
    expect((screen.getByLabelText('必须满足 1') as HTMLInputElement).value).toBe('新一代清单');
    expect(calls).toBe(2);
  });
});

describe('任务运行与保存影响', () => {
  test.each(['zh-CN', 'en', 'ja'] as const)('%s暂停任务不显示旧计划，启用任务继续显示计划', async (locale) => {
    const at = Date.UTC(2026, 10, 11, 12, 37, 35), t = createI18n(locale).t;
    const paused = view({ enabled: false, next_run_at: at }), active = view({ id: 18, name: '启用的相机', enabled: true, next_run_at: at });
    await mount(<TasksPage />, (request) => new URL(request.url).pathname === '/api/v1/tasks' && request.method === 'GET'
      ? json({ items: [paused, active], next_cursor: null, total: 2 }) : undefined, { locale });
    const planned = t('tasks.runtime.next', { time: formatDateTime(at, locale, 'Asia/Shanghai') });
    const row = await screen.findByRole('article', { name: paused.name });
    expect(within(row).getByText(t('tasks.paused'))).toBeTruthy();
    expect(within(row).queryByText(planned) === null).toBe(true);
    expect(within(screen.getByRole('article', { name: active.name })).getByText(planned)).toBeTruthy();
  });

  test('202仅表示接纳，暂停任务手动运行不发enabled更新', async () => {
    let accepted = false;
    const mounted = await mount(<TasksPage />, (request) => {
      const path = new URL(request.url).pathname;
      if (path === '/api/v1/tasks' && request.method === 'GET') return json({ items: [view({ enabled: false, runtime: { task_id: 17, phase: accepted ? 'queued' : 'idle', run_id: null, account_id: null, queued_at: null, started_at: null, blocked: null } })], next_cursor: null, total: 1 });
      if (path.endsWith('/run')) { accepted = true; return json({ task_id: 17, status: 'queued', run_id: null }, 202); }
      return undefined;
    });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '立即运行' }));
    await screen.findByText('上次运行请求已接纳，当前进度以上方状态为准。');
    await screen.findByText('已排队，尚未开始。');
    expect(mounted.requests.filter((request) => request.method === 'PATCH')).toHaveLength(0);
    expect(screen.getAllByText('已暂停').length).toBeGreaterThan(0);
  });

  test('409门禁拒绝保留任务，不显示排队成功，不自动重试', async () => {
    let writes = 0;
    await mount(<TasksPage />, (request) => {
      const path = new URL(request.url).pathname;
      if (path === '/api/v1/tasks' && request.method === 'GET') return json({ items: [view()], next_cursor: null, total: 1 });
      if (path.endsWith('/run')) { writes += 1; return failure(409); }
      return undefined;
    });
    await userEvent.setup().click(await screen.findByRole('button', { name: '立即运行' }));
    await screen.findByText('请求未完成，请重试。');
    expect(screen.queryByText('上次运行请求已接纳，当前进度以上方状态为准。')).toBeNull();
    expect(screen.queryByText('已排队，尚未开始。')).toBeNull();
    expect(screen.getByText('书房相机')).toBeTruthy();
    expect(writes).toBe(1);
  });

  test('复制只预填新建，自动账号不变固定；启停只写enabled，删除需确认', async () => {
    let enabled = true, deleted = false, created: Schema<'Task'> | null = null;
    const writes: { method: string; path: string; body: unknown }[] = [];
    await mount(<TasksPage />, (request) => {
      const path = new URL(request.url).pathname;
      if (path === '/api/v1/tasks' && request.method === 'GET') return json({ items: [...(deleted ? [] : [view({ enabled, account_id: 88, account_pinned: false })]), ...(created ? [view({ ...created, runtime: { task_id: created.id, phase: 'idle', run_id: null, account_id: null, queued_at: null, started_at: null, blocked: null } })] : [])], total: Number(!deleted) + Number(created !== null), next_cursor: null });
      if (path === '/api/v1/tasks/17' && request.method === 'GET') return json(view({ enabled, keyword: '刚更新的相机关键词', account_id: 99, account_pinned: false }));
      if (request.method === 'POST' && path === '/api/v1/tasks') return request.clone().json().then((body: unknown) => { writes.push({ method: request.method, path, body }); created = task({ id: 18, name: '复制的相机' }); return json(saved(created), 201); });
      if (request.method === 'PATCH') return request.clone().json().then((body: unknown) => { writes.push({ method: request.method, path, body }); enabled = false; return json(saved(task({ enabled }))); });
      if (request.method === 'DELETE') { writes.push({ method: request.method, path, body: null }); deleted = true; return new Response(null, { status: 204 }); }
      return undefined;
    });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '复制任务' }));
    expect(writes).toHaveLength(0);
    const name = await screen.findByLabelText('任务名称');
    expect((screen.getByLabelText('搜索关键词') as HTMLInputElement).value).toBe('刚更新的相机关键词');
    await user.clear(name); await user.type(name, '复制的相机');
    await user.click(screen.getByRole('button', { name: '保存任务' }));
    await screen.findByRole('article', { name: '复制的相机' });
    expect(writes[0]).toMatchObject({ method: 'POST', path: '/api/v1/tasks', body: { keyword: '刚更新的相机关键词', account_id: null, account_pinned: false, name: '复制的相机' } });
    expect(typeof writes[0]?.body === 'object' && writes[0]?.body !== null && Object.hasOwn(writes[0]?.body ?? {}, 'id')).toBe(false);
    let original = screen.getByRole('article', { name: '书房相机' });
    await user.click(within(original).getByRole('button', { name: '暂停' }));
    await waitFor(() => expect(writes[1]?.body).toEqual({ enabled: false }));
    original = screen.getByRole('article', { name: '书房相机' });
    await user.click(within(original).getByRole('button', { name: '删除任务' }));
    let dialog = await screen.findByRole('alertdialog', { name: '删除这个任务？' });
    expect(writes.filter((write) => write.method === 'DELETE')).toHaveLength(0);
    await user.click(within(dialog).getByRole('button', { name: '取消' }));
    await user.click(within(original).getByRole('button', { name: '删除任务' }));
    dialog = await screen.findByRole('alertdialog');
    await user.click(within(dialog).getByRole('button', { name: '删除任务' }));
    await waitFor(() => expect(screen.queryByRole('article', { name: '书房相机' })).toBeNull());
    expect(writes.filter((write) => write.method === 'DELETE')).toHaveLength(1);
    expect(screen.getByRole('article', { name: '复制的相机' })).toBeTruthy();
  });

  test('复制读取失败留在列表，再次读取可取消，晚到配置不能打开编辑器', async () => {
    let reads = 0, finish = (_response: Response) => {};
    const signals: AbortSignal[] = [];
    const mounted = await mount(<TasksPage />, (request) => {
      const path = new URL(request.url).pathname;
      if (path === '/api/v1/tasks' && request.method === 'GET') return json({ items: [view()], total: 1, next_cursor: null });
      if (path === '/api/v1/tasks/17' && request.method === 'GET') {
        reads += 1; signals.push(request.signal);
        return reads === 1 ? failure(503) : new Promise<Response>((resolve) => { finish = resolve; });
      }
      return undefined;
    });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '复制任务' }));
    await screen.findByText(/task-request-17/);
    expect(screen.getByRole('article', { name: '书房相机' })).toBeTruthy();
    expect(screen.queryByLabelText('搜索关键词')).toBeNull();
    expect(reads).toBe(1);
    await user.click(screen.getByRole('button', { name: '复制任务' }));
    await user.click(await screen.findByRole('button', { name: '取消' }));
    expect(signals[1]?.aborted).toBe(true);
    await act(async () => { finish(json(view({ keyword: '迟到配置' }))); await new Promise<void>((resolve) => setTimeout(resolve, 0)); });
    expect(screen.queryByLabelText('搜索关键词')).toBeNull();
    expect(screen.getByRole('article', { name: '书房相机' })).toBeTruthy();
    expect(mounted.requests.filter((request) => request.method !== 'GET')).toHaveLength(0);
    expect(reads).toBe(2);
  });

  test('列表输入法确认不应用筛选，组合结束后明确提交才保留完整搜索词', async () => {
    const change = mock(() => {});
    await mount(<TasksPage search={{ enabled: false }} onSearchChange={change} />);
    const input = await screen.findByLabelText('搜索任务');
    fireEvent.compositionStart(input);
    fireEvent.change(input, { target: { value: '  相机 256G  ' } });
    const keyAccepted = fireEvent.keyDown(input, { key: 'Enter', isComposing: true });
    expect(keyAccepted).toBe(false);
    const form = input.closest('form'); if (!form) throw new Error('搜索表单缺失');
    fireEvent.submit(form);
    expect(change).not.toHaveBeenCalled();
    fireEvent.compositionEnd(input);
    expect(change).not.toHaveBeenCalled();
    await userEvent.setup().click(screen.getByRole('button', { name: '搜索' }));
    expect(change).toHaveBeenCalledTimes(1);
    expect(change).toHaveBeenCalledWith({ enabled: false, q: '相机 256G', cursor: undefined });
  });

  test('新增排除词只有明确选择隐藏才POST，失败保留提示与选择', async () => {
    let writes = 0, body: unknown;
    await mount(<TaskSaveFeedback result={saved(task(), { rescored_count: 3, needs_reanalysis_count: 4, needs_reanalysis_ids: [1, 2, 3, 4], title_rejected_count: 2, title_rejected_ids: [5, 6], added_exclude_words: ['维修'], existing_exclude_match_count: 2 })} />, (request) => {
      if (new URL(request.url).pathname.endsWith('/apply-exclusions')) { writes += 1; return request.clone().json().then((value: unknown) => { body = value; return failure(409); }); }
      return undefined;
    });
    expect(writes).toBe(0);
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: '隐藏' }));
    await waitFor(() => expect(writes).toBe(1));
    expect(body).toEqual({ added_exclude_words: ['维修'] });
    await waitFor(() => expect(screen.getAllByText(/请求未完成，请重试/).length).toBeGreaterThan(0));
    expect(screen.getByRole('button', { name: '保留' })).toBeTruthy();
    await user.click(screen.getByRole('button', { name: '保留' }));
    expect(screen.queryByRole('button', { name: '隐藏' })).toBeNull();
    expect(writes).toBe(1);
  });

  test('跨页关键词只预填新建，不混入listTasks query，也不自动创建或运行', async () => {
    const change = mock(() => {});
    const mounted = await mount(<TasksPage search={{ create_keyword: '收音机', q: '已有任务' }} onSearchChange={change} />);
    expect((await screen.findByLabelText('搜索关键词') as HTMLInputElement).value).toBe('收音机');
    expect(mounted.requests.filter((request) => request.method === 'POST')).toHaveLength(0);
    const list = mounted.requests.find((request) => new URL(request.url).pathname === '/api/v1/tasks');
    expect(new URL(list?.url ?? 'http://localhost').searchParams.has('create_keyword')).toBe(false);
    expect(new URL(list?.url ?? 'http://localhost').searchParams.get('q')).toBe('已有任务');
    await userEvent.setup().click(screen.getByRole('button', { name: '取消' }));
    await waitFor(() => expect(change).toHaveBeenCalledWith({ q: '已有任务', create_keyword: undefined, task_id: undefined }));
  });

  test('空态直接提供模板，预填关键词时不会自动保存', async () => {
    const mounted = await mount(<TasksPage />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'iPad mini' }));
    expect((await screen.findByLabelText('搜索关键词') as HTMLInputElement).value).toBe('iPad mini');
    expect(mounted.requests.filter((request) => request.method === 'POST')).toHaveLength(0);
  });

  test('英文内置字典名称和解释翻译，自定义条目原文不翻译', async () => {
    const own: Schema<'AnalysisProfileFields'> = { ...profile, risks: [...profile.risks, { id: 'c8', name: '屏幕压伤原话', hint: '自定义判断原话', question: '卖家原话', attitude: 'mind', rev: 1, source: 'user', edited: true }] };
    await mount(<TaskEditor initialTask={task({ judge_mode: 'ai', notify_policy: 'good', profile: own })} onSaved={() => {}} />, undefined, { locale: 'en' });
    await screen.findByRole('radiogroup', { name: /ID lock/ });
    expect(screen.getByDisplayValue('屏幕压伤原话')).toBeTruthy();
    expect(screen.getByDisplayValue('自定义判断原话')).toBeTruthy();
    expect(screen.queryByText('账号没退出')).toBeNull();
  });
});

function affected(ids: number[], titleIDs: number[] = []): Partial<Schema<'TaskSaveEffects'>> {
  return { needs_reanalysis_ids: ids, needs_reanalysis_count: ids.length, title_rejected_ids: titleIDs, title_rejected_count: titleIDs.length };
}
function reanalysisPreview(ids: readonly number[]): Schema<'ReanalysisPreview'> {
  return { as_of: 1794355200000, requested_count: ids.length, cached_detail_count: ids.filter((id) => id <= 200).length, needs_detail_count: ids.filter((id) => id > 200).length };
}
function reanalysisResult(ids: readonly number[]): Schema<'ReanalysisResult'> {
  return { items: ids.map((finding_id) => ({ finding_id, status: 'queued', needs_detail: finding_id > 200, reason: null })), needs_detail_count: ids.filter((id) => id > 200).length };
}

describe('保存后的精确集合重新分析', () => {
  test('保存不触发重新分析；两集合各自预检并一次确认，215条按200和15顺序入队', async () => {
    const ids = Array.from({ length: 215 }, (_, index) => index + 1), previews: number[][] = [], batches: number[][] = [];
    await mount(<TaskSaveFeedback result={saved(task(), affected(ids, [900]))} />, async (request) => {
      const path = new URL(request.url).pathname;
      if (path.endsWith('/reanalysis-preview')) { const body: Schema<'FindingSelection'> = await request.json(); previews.push(body.ids); return json(reanalysisPreview(body.ids)); }
      if (path.endsWith('/findings/batch')) { const body: Schema<'FindingBatchRequest'> = await request.json(); expect(body.action).toBe('reanalyze'); batches.push(body.ids); return json(reanalysisResult(body.ids), 202); }
      return undefined;
    });
    expect(previews).toHaveLength(0); expect(batches).toHaveLength(0);
    const user = userEvent.setup();
    const needs = screen.getByRole('region', { name: '已有 215 个商品要重新分析才能判断这一条。' });
    await user.click(within(needs).getByRole('button', { name: '重新分析' }));
    const dialog = await screen.findByRole('alertdialog');
    expect(previews.map((batch) => batch.length)).toEqual([200, 15]);
    expect(dialog.textContent).toContain('200 个可使用缓存详情');
    expect(dialog.textContent).toContain('15 个需要重新打开详情');
    expect(dialog.textContent).toContain('重新分析可能调用模型');
    expect(dialog.textContent).toContain('不会预扣额度');
    expect(batches).toHaveLength(0);
    await user.click(within(dialog).getByRole('button', { name: '确认' }));
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());
    expect(batches).toEqual(previews);
    expect(batches.flat()).toEqual(ids);
    expect(within(needs).getByText('已接纳 215 条重新分析请求，请查看后续状态')).toBeTruthy();
    const titles = screen.getByRole('region', { name: '有 1 个商品此前在标题判断中被排除，需要重新分析才能按新条件判断。' });
    await user.click(within(titles).getByRole('button', { name: '重新分析' }));
    const titleDialog = await screen.findByRole('alertdialog');
    expect(previews.at(-1)).toEqual([900]);
    expect(batches.flat()).not.toContain(900);
    await user.click(within(titleDialog).getByRole('button', { name: '确认' }));
    await waitFor(() => expect(batches.at(-1)).toEqual([900]));
    expect(within(needs).getByText('已接纳 215 条重新分析请求，请查看后续状态')).toBeTruthy();
  });

  test('空集合不显示动作；一次普通保存也不预检或入队', async () => {
    const onSaved = mock(() => {});
    const mounted = await mount(<TaskEditor initialTask={task()} onSaved={onSaved} />, (request) => request.method === 'PATCH' ? json(saved(task(), affected([31], [32]))) : undefined);
    expect(screen.queryByRole('button', { name: '重新分析' })).toBeNull();
    await userEvent.setup().click(await screen.findByRole('button', { name: '保存任务' }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(mounted.requests.some((request) => new URL(request.url).pathname.includes('/findings'))).toBe(false);
  });

  test.each([503, 401])('预检%s不弹确认、不入队、不自动重试', async (status) => {
    let previews = 0, batches = 0;
    const signals: AbortSignal[] = [];
    await mount(<TaskSaveFeedback result={saved(task(), affected([1]))} />, (request) => {
      const path = new URL(request.url).pathname;
      if (path.endsWith('/reanalysis-preview')) { previews += 1; signals.push(request.signal); return status === 401 ? json({ error: { code: 'auth.unauthenticated', args: {} }, fields: [], request_id: 'lost-session' } satisfies Schema<'ErrorEnvelope'>, 401) : failure(status); }
      if (path.endsWith('/findings/batch')) { batches += 1; return json(reanalysisResult([1]), 202); }
      return undefined;
    });
    await userEvent.setup().click(screen.getByRole('button', { name: '重新分析' }));
    await waitFor(() => expect(screen.getByRole('button', { name: '重新分析' }).hasAttribute('aria-busy')).toBe(false));
    expect(previews).toBe(1); expect(batches).toBe(0);
    expect(screen.queryByRole('alertdialog')).toBeNull();
    if (status === 401) expect(signals[0]?.aborted).toBe(true);
    else expect(screen.getByText(/task-request-17/)).toBeTruthy();
  });

  test('取消预检忽略晚响应；下一次只检查另一精确集合，不沿用旧预览', async () => {
    let finish = (_response: Response) => {};
    const previews: number[][] = [], signals: AbortSignal[] = [];
    await mount(<TaskSaveFeedback result={saved(task(), affected([1], [900]))} />, async (request) => {
      if (new URL(request.url).pathname.endsWith('/reanalysis-preview')) {
        const body: Schema<'FindingSelection'> = await request.json(); previews.push(body.ids); signals.push(request.signal);
        return previews.length === 1 ? new Promise<Response>((resolve) => { finish = resolve; }) : json(reanalysisPreview(body.ids));
      }
      return undefined;
    });
    const user = userEvent.setup();
    await user.click(within(screen.getByRole('region', { name: '已有 1 个商品要重新分析才能判断这一条。' })).getByRole('button', { name: '重新分析' }));
    await waitFor(() => expect(signals).toHaveLength(1));
    await user.click(screen.getByRole('button', { name: '取消' }));
    expect(signals[0]?.aborted).toBe(true);
    await act(async () => { finish(json(reanalysisPreview([1]))); await new Promise<void>((resolve) => setTimeout(resolve, 0)); });
    expect(screen.queryByRole('alertdialog')).toBeNull();
    await user.click(within(screen.getByRole('region', { name: '有 1 个商品此前在标题判断中被排除，需要重新分析才能按新条件判断。' })).getByRole('button', { name: '重新分析' }));
    expect((await screen.findByRole('alertdialog')).textContent).toContain('1 个需要重新打开详情');
    expect(previews).toEqual([[1], [900]]);
  });

  test('第二批失败保留逐项状态与原因，只把真实queued计为接纳，未确认批不冒充成功', async () => {
    const ids = Array.from({ length: 215 }, (_, index) => index + 1), batches: number[][] = [];
    await mount(<TaskSaveFeedback result={saved(task(), affected(ids))} />, async (request) => {
      const path = new URL(request.url).pathname;
      if (path.endsWith('/reanalysis-preview')) { const body: Schema<'FindingSelection'> = await request.json(); return json(reanalysisPreview(body.ids)); }
      if (path.endsWith('/findings/batch')) {
        const body: Schema<'FindingBatchRequest'> = await request.json(); batches.push(body.ids);
        return batches.length === 2 ? failure(503) : json({ items: body.ids.map((finding_id) => ({ finding_id, status: finding_id === 1 ? 'cached' : finding_id === 2 ? 'rejected' : 'queued', reason: finding_id === 2 ? { code: 'findings.not_found', args: {} } : null, needs_detail: false })), needs_detail_count: 0 } satisfies Schema<'ReanalysisResult'>, 202);
      }
      return undefined;
    });
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: '重新分析' }));
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: '确认' }));
    await screen.findByText('操作已停止：已确认 200 条结果，其中 198 条已排队；15 条请求结果未确认，0 条尚未提交。未自动重发。');
    expect(screen.getByText('已接纳 198 条重新分析请求，请查看后续状态')).toBeTruthy();
    expect(screen.getByText((_, element) => element?.tagName === 'LI' && element.textContent?.startsWith('#1 · 已使用缓存') === true)).toBeTruthy();
    expect(screen.getByText((_, element) => element?.tagName === 'LI' && element.textContent?.startsWith('#2 · 未通过') === true).textContent).toContain('这条结果已不存在');
    expect(screen.queryByText((_, element) => element?.tagName === 'LI' && element.textContent?.startsWith('#215 ·') === true)).toBeNull();
    expect(batches.map((batch) => batch.length)).toEqual([200, 15]);
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });

  test.each(['取消', '认证失效', '反馈换代'] as const)('第二批在途%s，已确认批保留，迟到响应不写入新状态且不重发', async (mode) => {
    const ids = Array.from({ length: 215 }, (_, index) => index + 1);
    let finish = (_response: Response) => {}, batches = 0;
    const signals: AbortSignal[] = [];
    function FeedbackHarness() {
      const [value, setValue] = useState(saved(task(), affected(ids)));
      return <><button onClick={() => setValue(saved(task({ updated_at: 1794355200001 }), affected([900])))}>更换保存反馈</button><TaskSaveFeedback result={value} /></>;
    }
    await mount(<AuthenticationHarness><FeedbackHarness /></AuthenticationHarness>, async (request) => {
      const path = new URL(request.url).pathname;
      if (path.endsWith('/reanalysis-preview')) { const body: Schema<'FindingSelection'> = await request.json(); return json(reanalysisPreview(body.ids)); }
      if (path.endsWith('/findings/batch')) {
        const body: Schema<'FindingBatchRequest'> = await request.json(); batches += 1; signals.push(request.signal);
        return batches === 1 ? json(reanalysisResult(body.ids), 202) : new Promise<Response>((resolve) => { finish = resolve; });
      }
      return undefined;
    });
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: '重新分析' }));
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: '确认' }));
    await waitFor(() => expect(signals).toHaveLength(2));
    if (mode === '取消') await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: '取消' }));
    else if (mode === '认证失效') fireEvent.click(screen.getByText('使认证失效', { selector: 'button' }));
    else fireEvent.click(screen.getByText('更换保存反馈', { selector: 'button' }));
    expect(signals[1]?.aborted).toBe(true);
    await act(async () => { finish(json(reanalysisResult(ids.slice(200)), 202)); await new Promise<void>((resolve) => setTimeout(resolve, 0)); });
    expect(screen.queryByRole('alertdialog')).toBeNull();
    expect(screen.queryByText((_, element) => element?.tagName === 'LI' && element.textContent?.startsWith('#215 ·') === true)).toBeNull();
    if (mode !== '反馈换代') expect(screen.getByText('已接纳 200 条重新分析请求，请查看后续状态')).toBeTruthy();
    else expect(screen.getByRole('region', { name: '已有 1 个商品要重新分析才能判断这一条。' })).toBeTruthy();
    if (mode === '认证失效') { await user.click(screen.getByRole('button', { name: '恢复认证' })); expect(batches).toBe(2); }
    expect(batches).toBe(2);
  });
});

const taskFileText = '{"format":"kakatta.tasks","version":1,"tasks":[{"keyword":"相机","min_price":0.01}]}\n';
const importedItem: Schema<'TaskImportItem'> = { index: 1, status: 'imported', task_id: 71, corrections: [], warnings: [], reason: null, fields: [] };
const completedImport: Schema<'TaskImportReport'> = { total: 1, imported: 1, skipped: 0, failed: 0, unprocessed: 0, complete: true, failure: null, items: [importedItem] };

async function selectTaskFile(user: ReturnType<typeof userEvent.setup>, file = new File([taskFileText], 'tasks.json', { type: 'application/json' })) {
  await user.click(await screen.findByRole('button', { name: '导入任务' }));
  const dialog = await screen.findByRole('dialog', { name: '导入任务' });
  const input = within(dialog).getByLabelText<HTMLInputElement>('任务 JSON 文件');
  expect(within(dialog).getByRole<HTMLButtonElement>('button', { name: '导入任务' }).disabled).toBe(true);
  fireEvent.change(input, { target: { files: [file] } });
  return { dialog, input };
}

describe('任务配置导入导出', () => {
  test('上传原文件和唯一multipart字段，200部分报告展示真实修正、提示、失败和未处理数量', async () => {
    const file = new File([taskFileText], 'tasks-original.json', { type: 'application/json' });
    const textRead = spyOn(file, 'text');
    const bytesRead = spyOn(file, 'arrayBuffer');
    const posts: Request[] = [];
    let listReads = 0;
    const storageFailure = { code: 'service.storage_failure', args: {} };
    const report: Schema<'TaskImportReport'> = {
      total: 4, imported: 1, skipped: 1, failed: 1, unprocessed: 1, complete: false, failure: storageFailure,
      items: [
        { ...importedItem, corrections: [{ key: 'name', reason: { code: 'tasks.import_name_defaulted', args: {} } }], warnings: [{ key: 'interval_minutes', reason: { code: 'settings.outside_recommendation', args: {} } }] },
        { index: 2, status: 'skipped', task_id: null, reason: { code: 'tasks.import_invalid_file', args: {} }, corrections: [], warnings: [], fields: [{ key: 'keyword', reason: { code: 'tasks.text_length', args: { min: 1, max: 50 } } }] },
        { index: 3, status: 'failed', task_id: null, reason: storageFailure, corrections: [], warnings: [], fields: [] },
      ],
    };
    try {
      await mount(<TasksPage />, (request) => {
        const path = new URL(request.url).pathname;
        if (path === '/api/v1/tasks/import') { posts.push(request.clone()); return json(report); }
        if (path === '/api/v1/tasks') listReads += 1;
        return undefined;
      });
      const user = userEvent.setup();
      const { dialog } = await selectTaskFile(user, file);
      expect(posts).toHaveLength(0);
      expect(textRead).not.toHaveBeenCalled();
      expect(bytesRead).not.toHaveBeenCalled();
      await user.click(within(dialog).getByRole('button', { name: '导入任务' }));
      await within(dialog).findByText('共 4 项，已导入 1 项，跳过 1 项，失败 1 项，未处理 1 项。');
      expect(posts).toHaveLength(1);
      expect(textRead).not.toHaveBeenCalled();
      expect(bytesRead).not.toHaveBeenCalled();
      const request = posts[0]; if (!request) throw new Error('没有收到上传');
      expect(request.method).toBe('POST');
      expect(request.headers.get('Content-Type')).toMatch(/^multipart\/form-data; boundary=/i);
      const form = await request.formData();
      expect([...form.keys()]).toEqual(['file']);
      const upload = form.get('file'); if (!upload || typeof upload === 'string') throw new Error('没有收到文件');
      expect(upload.name).toBe(file.name);
      expect(await upload.text()).toBe(taskFileText);
      expect(within(dialog).getByText('导入未完成，已保存的任务仍然保留。')).toBeTruthy();
      expect(within(dialog).getByText(/第 1 项已导入.*#71/)).toBeTruthy();
      expect(within(dialog).getByText('第 2 项已跳过。')).toBeTruthy();
      expect(within(dialog).getByText('第 3 项保存失败。')).toBeTruthy();
      expect(within(dialog).getByText(/名称为空，已使用搜索关键词/)).toBeTruthy();
      expect(within(dialog).getByText(/超出推荐范围/)).toBeTruthy();
      expect(within(dialog).getByText(/请输入 1 到 50 个字符/)).toBeTruthy();
      expect(within(dialog).queryByText(/第 4 项/)).toBeNull();
      expect(within(dialog).queryByRole('button', { name: '导入任务' })).toBeNull();
      await waitFor(() => expect(listReads).toBeGreaterThan(1));
      expect(posts).toHaveLength(1);
    } finally { textRead.mockRestore(); bytesRead.mockRestore(); }
  });

  test('完成报告仅展示真实成功数量，关闭后重新打开需要重新选文件', async () => {
    let writes = 0;
    await mount(<TasksPage />, (request) => {
      if (new URL(request.url).pathname === '/api/v1/tasks/import') { writes += 1; return json(completedImport); }
      return undefined;
    });
    const user = userEvent.setup(), { dialog } = await selectTaskFile(user);
    await user.click(within(dialog).getByRole('button', { name: '导入任务' }));
    await within(dialog).findByText('共 1 项，已导入 1 项，跳过 0 项，失败 0 项，未处理 0 项。');
    expect(within(dialog).queryByText(/导入未完成/)).toBeNull();
    await user.click(within(dialog).getByRole('button', { name: '关闭' }));
    await user.click(screen.getByRole('button', { name: '导入任务' }));
    const next = await screen.findByRole('dialog', { name: '导入任务' });
    expect(within(next).getByRole<HTMLButtonElement>('button', { name: '导入任务' }).disabled).toBe(true);
    expect(within(next).queryByText('tasks.json')).toBeNull();
    expect(writes).toBe(1);
  });

  test.each([[400, 'tasks.import_unsupported_version', '不支持这个任务配置文件版本'], [401, 'auth.unauthenticated', '请先登录']] as const)('导入HTTP %s显示服务端原因并保留文件，认证恢复不重发', async (status, code, message) => {
    let writes = 0;
    const mounted = await mount(<TasksPage />, (request) => {
      if (new URL(request.url).pathname === '/api/v1/tasks/import') {
        writes += 1;
        return json({ error: { code, args: {} }, fields: [], request_id: 'import-request-41' } satisfies Schema<'ErrorEnvelope'>, status);
      }
      return undefined;
    });
    const file = new File(['{"version":99}'], 'future.json', { type: 'application/json' });
    const user = userEvent.setup(), { dialog, input } = await selectTaskFile(user, file);
    await user.click(within(dialog).getByRole('button', { name: '导入任务' }));
    await within(dialog).findByText(message);
    expect(within(dialog).getByText(/import-request-41/)).toBeTruthy();
    expect(input.files?.[0]).toBe(file);
    expect(within(dialog).queryByText(/已导入 \d+ 项/)).toBeNull();
    await act(async () => { mounted.api.restoreAuthentication(); });
    expect(writes).toBe(1);
  });

  test.each(['取消', '认证失效'] as const)('上传在途%s时保留结果未确认提示，迟到报告不覆盖，刷新不重发', async (mode) => {
    const signals: AbortSignal[] = [];
    let finish = (_response: Response) => {};
    let listReads = 0;
    await mount(<AuthenticationHarness><TasksPage /></AuthenticationHarness>, (request) => {
      const path = new URL(request.url).pathname;
      if (path === '/api/v1/tasks/import') {
        signals.push(request.signal);
        return new Promise<Response>((resolve) => { finish = resolve; });
      }
      if (path === '/api/v1/tasks') listReads += 1;
      return undefined;
    });
    const user = userEvent.setup(), { dialog } = await selectTaskFile(user);
    await user.click(within(dialog).getByRole('button', { name: '导入任务' }));
    await waitFor(() => expect(signals).toHaveLength(1));
    if (mode === '取消') await user.click(within(dialog).getByRole('button', { name: '取消' }));
    else fireEvent.click(screen.getByText('使认证失效', { selector: 'button' }));
    expect(signals[0]?.aborted).toBe(true);
    await within(dialog).findByText(/尚不能确认已导入多少任务/);
    await act(async () => { finish(json(completedImport)); await new Promise<void>((resolve) => setTimeout(resolve, 0)); });
    expect(within(dialog).queryByText(/已导入 1 项/)).toBeNull();
    expect(within(dialog).queryByRole('button', { name: '导入任务' })).toBeNull();
    if (mode === '认证失效') fireEvent.click(screen.getByText('恢复认证', { selector: 'button' }));
    await waitFor(() => expect(listReads).toBeGreaterThan(1));
    expect(signals).toHaveLength(1);
  });

  test('在途导入阻挡跨页，取消离开保持上传，明确离开才终止传输', async () => {
    const signals: AbortSignal[] = [];
    let finish = (_response: Response) => {};
    const mounted = await mount(<TasksPage />, (request) => {
      if (new URL(request.url).pathname === '/api/v1/tasks/import') {
        signals.push(request.signal);
        return new Promise<Response>((resolve) => { finish = resolve; });
      }
      return undefined;
    }, { protectUnsaved: true });
    const user = userEvent.setup(), { dialog } = await selectTaskFile(user);
    await user.click(within(dialog).getByRole('button', { name: '导入任务' }));
    await waitFor(() => expect(signals).toHaveLength(1));
    act(() => { void mounted.router.navigate({ to: '/overview' }); });
    const guard = await screen.findByRole('alertdialog', { name: '放弃未保存的修改？' });
    await user.click(within(guard).getByRole('button', { name: '取消' }));
    expect(signals[0]?.aborted).toBe(false);
    expect(mounted.router.state.location.pathname).toBe('/');
    act(() => { void mounted.router.navigate({ to: '/overview' }); });
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: '放弃修改并离开' }));
    await screen.findByRole('heading', { name: '目标页' });
    expect(signals[0]?.aborted).toBe(true);
    await act(async () => { finish(json(completedImport)); });
    expect(signals).toHaveLength(1);
  });

  test('导出全量GET不传筛选，原JSON Blob交下载，危险文件名回退到契约名', async () => {
    const requests: Request[] = [], downloads: string[] = [], blobs: Blob[] = [];
    const raw = '{"format":"kakatta.tasks","version":1,"exported_at":"2026-11-11T06:00:00+08:00","tasks":[]}\n';
    const objectURL = spyOn(URL, 'createObjectURL').mockImplementation((blob) => {
      if (!(blob instanceof Blob)) throw new Error('任务下载必须使用 Blob');
      blobs.push(blob);
      return 'blob:tasks-test';
    });
    const click = spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) { downloads.push(this.download); });
    try {
      await mount(<TasksPage search={{ q: '相机', enabled: true, cursor: 'next' }} />, (request) => {
        if (new URL(request.url).pathname !== '/api/v1/tasks/export') return undefined;
        requests.push(request.clone());
        return new Response(raw, { headers: { 'Content-Type': 'application/json; charset=utf-8', 'Content-Disposition': 'attachment; filename="../unsafe.json"' } });
      });
      const user = userEvent.setup();
      await user.click(await screen.findByRole('button', { name: '导出任务' }));
      const dialog = await screen.findByRole('dialog', { name: '导出任务' });
      expect(within(dialog).getByText('导出全部已保存的任务配置。')).toBeTruthy();
      expect(requests).toHaveLength(0);
      await user.click(within(dialog).getByRole('button', { name: '导出任务' }));
      await waitFor(() => expect(downloads).toEqual(['kakatta-tasks.json']));
      expect(requests).toHaveLength(1);
      expect(requests[0]?.method).toBe('GET');
      expect(new URL(requests[0]?.url ?? 'http://localhost').search).toBe('');
      expect(await blobs[0]?.text()).toBe(raw);
      expect(screen.queryByRole('dialog', { name: '导出任务' })).toBeNull();
    } finally { objectURL.mockRestore(); click.mockRestore(); }
  });

  test.each(['空文件', '错误媒体类型', '认证失败', '取消'] as const)('导出%s不交下载，也不自动重试', async (mode) => {
    const signals: AbortSignal[] = [];
    let finish = (_response: Response) => {};
    const objectURL = spyOn(URL, 'createObjectURL');
    try {
      await mount(<TasksPage />, (request) => {
        if (new URL(request.url).pathname !== '/api/v1/tasks/export') return undefined;
        signals.push(request.signal);
        if (mode === '取消') return new Promise<Response>((resolve) => { finish = resolve; });
        if (mode === '认证失败') return json({ error: { code: 'auth.unauthenticated', args: {} }, fields: [], request_id: 'export-auth' } satisfies Schema<'ErrorEnvelope'>, 401);
        return mode === '空文件' ? new Response('', { headers: { 'Content-Type': 'application/json' } }) : new Response('<html></html>', { headers: { 'Content-Type': 'text/html' } });
      });
      const user = userEvent.setup();
      await user.click(await screen.findByRole('button', { name: '导出任务' }));
      const dialog = await screen.findByRole('dialog', { name: '导出任务' });
      await user.click(within(dialog).getByRole('button', { name: '导出任务' }));
      if (mode === '取消') {
        await user.click(within(dialog).getByRole('button', { name: '取消' }));
        expect(signals[0]?.aborted).toBe(true);
        await act(async () => { finish(new Response(taskFileText, { headers: { 'Content-Type': 'application/json' } })); });
      } else await within(dialog).findByText(mode === '认证失败' ? '请先登录' : '服务端返回了无法读取的响应。');
      expect(objectURL).not.toHaveBeenCalled();
      expect(signals).toHaveLength(1);
    } finally { objectURL.mockRestore(); }
  });
});

describe('需求清单的需求基线', () => {
  test.each(['zh-CN', 'en', 'ja'] as const)('新建任务套模板和改需求时不提示更新不存在的清单（%s）', async (locale) => {
    const i18n = createI18n(locale);
    await mount(<TaskEditor initialFields={fields({ judge_mode: 'ai', notify_policy: 'good', profile: { ...profile, musts: [], risks: profile.risks.filter((risk) => risk.source === 'system') } })} onSaved={() => {}} />, undefined, { locale });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'MacBook Pro' }));
    const requirement = screen.getByRole('textbox', { name: i18n.t('tasks.field.requirement') });
    expect((requirement as HTMLTextAreaElement).value).not.toBe('');
    expect(screen.queryByText(i18n.t('tasks.profile.requirement_changed')) === null).toBe(true);
    expect(screen.queryByRole('button', { name: i18n.t('tasks.profile.keep_current') }) === null).toBe(true);
    await user.type(requirement, '，16GB');
    expect(screen.queryByText(i18n.t('tasks.profile.requirement_changed')) === null).toBe(true);
  });

  test('首次生成成功后改需求才提示，选择保留后再次修改仍提示', async () => {
    const generated: Schema<'GeneratedDraft'> = { profile, warnings: [], suggestions: { keyword: null, exclude_words: [], budget_min: null, budget_max: null } };
    await mount(<TaskEditor initialFields={fields({ judge_mode: 'ai', notify_policy: 'good', keyword: '相机' })} onSaved={() => {}} />, (request) => new URL(request.url).pathname === '/api/v1/ai/profile' ? json(generated) : undefined, { providers: [provider] });
    const user = userEvent.setup();
    const requirement = await screen.findByRole('textbox', { name: '我的要求' });
    await user.type(requirement, '国行');
    expect(screen.queryByRole('button', { name: '保持现在的清单' }) === null).toBe(true);
    await user.click(screen.getByRole('button', { name: '生成清单' }));
    await screen.findByLabelText('必须满足 1');
    expect(screen.queryByRole('button', { name: '保持现在的清单' }) === null).toBe(true);
    await user.type(requirement, '，有发票');
    await user.click(await screen.findByRole('button', { name: '保持现在的清单' }));
    expect(screen.queryByRole('button', { name: '保持现在的清单' }) === null).toBe(true);
    await user.type(requirement, '，低价');
    expect(screen.getByRole('button', { name: '保持现在的清单' })).toBeDefined();
  });

  test('编辑已有清单仍会提示需求变化', async () => {
    await mount(<TaskEditor initialTask={task({ judge_mode: 'ai', notify_policy: 'good', profile, requirement: '国行' })} onSaved={() => {}} />);
    await userEvent.setup().type(await screen.findByRole('textbox', { name: '我的要求' }), '，有发票');
    expect(screen.getByRole('button', { name: '保持现在的清单' })).toBeDefined();
  });
});
