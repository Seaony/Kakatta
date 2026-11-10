import { afterEach, describe, expect, mock, test } from 'bun:test';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClientProvider, type QueryClient } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ApiProvider } from '../../api/hooks';
import { createApiClient, type ApiFetch } from '../../api/client';
import type { components } from '../../api/schema.gen';
import { createAppQueryClient } from '../../app/query-client';
import { createI18n, I18nProvider, type Locale } from '../../i18n';
import { OverviewPage } from './overview-page';
import { bucketTime, percent, statisticalPrice } from './format';

type Overview = components['schemas']['OverviewView'];
const clients: QueryClient[] = [];
afterEach(() => { clients.splice(0).forEach((client) => client.clear()); });

function fixture(): Overview {
  const from = Date.parse('2026-11-01T04:00:00Z');
  const to = Date.parse('2026-11-02T05:00:00Z');
  return {
    period: 'today', timezone: 'America/New_York', from_at: from, to_at: to - 1, as_of: to - 1,
    coverage: { from_at: from, to_at: to - 1, is_partial: true },
    cards: { scanned_count: 731, new_count: 39, recommended_count: 12, recommended_ai_count: 9, recommended_keyword_count: 3, price_drop_count: 2 },
    comparison: {
      from_at: from - 86_400_000, to_at: from,
      scanned: { previous_count: 701, delta: 30, ratio: 0.0428 },
      new: { previous_count: 49, delta: -10, ratio: null },
      recommended: { previous_count: 50, delta: 3, ratio: 0.06 },
      price_drop: { previous_count: 0, delta: 2, ratio: null },
    },
    activity: Array.from({ length: 25 }, (_, index) => ({ from_at: from + index * 3_600_000, to_at: from + (index + 1) * 3_600_000, new_count: index % 3, seen_count: index % 5 })),
    tasks: [{
      task_id: 17, name: '便携录音机', judge_mode: 'ai', state: 'paused', reason: { code: 'run.account_needs_verification', args: {} },
      success_rate: 0.875, average_duration_ms: 1_234, recommendation_rate: 0.25, next_run_at: null,
      scanned_count: 531, new_finding_count: 7, recommended_count: 3, min_price: null,
      trend: [{ day: '2026-10-30', median: 14250.5, sample: 5 }, { day: '2026-10-31', median: null, sample: 0 }, { day: '2026-11-01', median: 13900, sample: 6 }],
    }],
    grade_distribution: ['steal', 'good', 'fair', 'pricey', 'avoid', 'unpriced', 'unmatched', 'junk'].map((kind, index) => ({ kind, count: index + 1 })),
    accounts: { total: 2, active: 1 }, ai: { count: 23, success_rate: 0.91, average_duration_ms: 1_237, tokens: 18271 },
    notifications: { count: 5, success_rate: null },
    exceptions: { risk_failure_count: 2, network_failure_count: 1, unresolved_events: [{ id: 8, kind: 'account_verify', subject_id: 7, message: { code: 'run.account_needs_verification', args: {} }, created_at: from, resolved_at: null, notified_at: from }] },
  };
}

function openPage(fetch: ApiFetch, locale: Locale = 'zh-CN') {
  const api = createApiClient({ fetch });
  const client = createAppQueryClient();
  clients.push(client);
  const root = createRootRoute({ component: () => <OverviewPage /> });
  const router = createRouter({ routeTree: root, history: createMemoryHistory({ initialEntries: ['/'] }) });
  return render(<I18nProvider instance={createI18n(locale)}><ApiProvider api={api}><QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider></ApiProvider></I18nProvider>);
}

function deferred<T>() {
  let resolve: (value: T) => void = () => {};
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

describe('监控概览真实协议', () => {
  test('统计显示转换保留分单位和比率，并区分重复民用小时', () => {
    expect(percent(0.1, 'zh-CN')).toBe('10%');
    expect(statisticalPrice(14250.5, 'zh-CN')).toContain('142.505');
    expect(bucketTime(Date.parse('2026-11-01T05:00:00Z'), 'en', 'America/New_York')).toContain('GMT-4');
    expect(bucketTime(Date.parse('2026-11-01T06:00:00Z'), 'en', 'America/New_York')).toContain('GMT-5');
  });

  test('统计价格完整显示半分和四分之一分，普通整数分显示不变', () => {
    expect(statisticalPrice(100.5, 'zh-CN')).toBe('¥1.005');
    expect(statisticalPrice(100.25, 'zh-CN')).toBe('¥1.0025');
    expect(statisticalPrice(100, 'zh-CN')).toBe('¥1.00');
    expect(statisticalPrice(101, 'zh-CN')).toBe('¥1.01');
    expect(statisticalPrice(100.5, 'en')).toBe('CN¥1.005');
    expect(statisticalPrice(100.25, 'en')).toBe('CN¥1.0025');
  });

  test('直接展示聚合、比例、空值及25个真实小时，保留半分和走势缺口', async () => {
    const requests: Request[] = [];
    openPage(async (request) => { requests.push(request); return Response.json(fixture()); });
    const scanned = await screen.findByRole('region', { name: '扫描商品' });
    expect(scanned.textContent).toContain('731');
    expect(requests).toHaveLength(1);
    expect(requests[0]?.method).toBe('GET');
    expect(new URL(requests[0]!.url).pathname).toBe('/api/v1/overview');
    expect(new URL(requests[0]!.url).searchParams.get('period')).toBe('today');
    expect(screen.getByRole('region', { name: '推荐' }).textContent).toContain('+6%');
    expect(screen.getByRole('region', { name: '新上架' }).textContent).toContain('-10');
    expect(screen.getByText('所选期间的数据不完整，只能统计仍保留的记录。')).toBeTruthy();
    fireEvent.click(screen.getByText('查看检查活动数据'));
    const activity = screen.getByRole('table', { name: '检查活动' });
    expect(within(activity).getAllByRole('row')).toHaveLength(26);
    expect(activity.textContent).toContain('GMT-4');
    expect(activity.textContent).toContain('GMT-5');
    const performance = screen.getByRole('region', { name: '任务表现' });
    expect(performance.textContent).toContain('25%');
    expect(performance.textContent).toContain('暂不可用');
    expect(performance.textContent).not.toContain('¥0.00');
    fireEvent.click(within(performance).getByText('查看「便携录音机」的价格数据', { selector: 'summary' }));
    expect(within(performance).getByRole('table').textContent).toContain('142.505');
    expect(within(performance).getAllByRole('row')).toHaveLength(4);
    const trend = within(performance).getByRole('img');
    expect(trend.querySelectorAll('circle')).toHaveLength(2);
    expect(trend.querySelectorAll('line')).toHaveLength(0);
    expect(screen.getByRole('region', { name: 'AI 调用' }).textContent).toContain('91%');
    const events = screen.getByRole('region', { name: '异常' });
    expect(within(events).getByRole('link', { name: '查看处理入口' }).getAttribute('href')).toContain('account_id=7');
  });

  test('换期间使用新query，慢旧响应不覆盖新期间', async () => {
    const week = deferred<Response>();
    const requests: string[] = [];
    openPage(async (request) => {
      const period = new URL(request.url).searchParams.get('period') ?? '';
      requests.push(period);
      if (period === '7d') return week.promise;
      const data = fixture(); data.period = period;
      if (period === '30d') { data.cards.scanned_count = 3029; data.comparison = null; }
      return Response.json(data);
    });
    await screen.findByRole('region', { name: '扫描商品' });
    const user = userEvent.setup();
    act(() => screen.getByRole('radio', { name: '今天' }).focus());
    await user.keyboard('[ArrowRight>]');
    await waitFor(() => expect(requests).toContain('7d'));
    await user.keyboard('[/ArrowRight]');
    expect(screen.queryByRole('region', { name: '扫描商品' })).toBeNull();
    await user.click(screen.getByRole('radio', { name: '近 30 天' }));
    await waitFor(() => expect(screen.getByRole('region', { name: '扫描商品' }).textContent).toContain('3,029'));
    const late = fixture(); late.period = '7d'; late.cards.scanned_count = 1703;
    week.resolve(Response.json(late));
    await waitFor(() => expect(screen.getByRole('radio', { name: '近 30 天' }).getAttribute('data-state')).toBe('checked'));
    expect(screen.getByRole('region', { name: '扫描商品' }).textContent).toContain('3,029');
    expect(screen.queryByText('1,703')).toBeNull();
    expect(within(screen.getByRole('region', { name: '扫描商品' })).queryByText('没有足够的上一期间数据。')).toBeNull();
  });

  test('夏令时开始日完整保留服务端的23个UTC桶', async () => {
    const data = fixture();
    const from = Date.parse('2026-03-08T05:00:00Z');
    data.from_at = from; data.to_at = from + 23 * 3_600_000 - 1; data.as_of = data.to_at;
    data.activity = Array.from({ length: 23 }, (_, index) => ({ from_at: from + index * 3_600_000, to_at: from + (index + 1) * 3_600_000, new_count: 0, seen_count: 0 }));
    openPage(async () => Response.json(data));
    await screen.findByRole('region', { name: '扫描商品' });
    fireEvent.click(screen.getByText('查看检查活动数据'));
    const activity = screen.getByRole('table', { name: '检查活动' });
    expect(within(activity).getAllByRole('row')).toHaveLength(24);
    expect(activity.textContent).toContain('GMT-5');
    expect(activity.textContent).toContain('GMT-4');
    expect(screen.getByText('这个期间没有检查活动。')).toBeTruthy();
  });

  test('初次错误使用安全原因并显式重试，不自动发请求', async () => {
    let calls = 0;
    openPage(async () => {
      calls++;
      return calls === 1 ? Response.json({ error: { code: 'private.unknown', args: {} }, fields: [], request_id: 'overview-test-request' }, { status: 503 }) : Response.json(fixture());
    });
    expect(await screen.findByRole('alert')).toHaveProperty('textContent', '无法读取监控概览请求未完成，请重试。重试');
    expect(calls).toBe(1);
    await userEvent.setup().click(screen.getByRole('button', { name: '重试' }));
    await screen.findByRole('region', { name: '扫描商品' });
    expect(calls).toBe(2);
    expect(screen.queryByText('private.unknown')).toBeNull();
  });

  test('刷新失败保留真实旧内容与当前期间，恢复只重试读取', async () => {
    let calls = 0;
    const transport = mock(async () => {
      calls++;
      return calls === 2 ? Response.json({ error: { code: 'web.network_failed', args: {} }, fields: [], request_id: 'refresh-test' }, { status: 503 }) : Response.json(fixture());
    });
    openPage(transport);
    await screen.findByRole('region', { name: '扫描商品' });
    await userEvent.setup().click(screen.getByRole('button', { name: '刷新概览' }));
    await screen.findByText('更新失败，仍显示上次的数据。');
    expect(screen.getByRole('region', { name: '扫描商品' }).textContent).toContain('731');
    expect(calls).toBe(2);
    await userEvent.setup().click(screen.getByRole('button', { name: '重试' }));
    await waitFor(() => expect(screen.queryByText('更新失败，仍显示上次的数据。')).toBeNull());
    expect(calls).toBe(3);
  });

  test('读取中卸载传递取消信号，不发任何写操作', async () => {
    let activeSignal: AbortSignal | undefined;
    const pending = deferred<Response>();
    const view = openPage(async (request) => { activeSignal = request.signal; expect(request.method).toBe('GET'); return pending.promise; });
    await waitFor(() => expect(activeSignal).toBeDefined());
    view.unmount();
    expect(activeSignal?.aborted).toBe(true);
    pending.resolve(Response.json(fixture()));
  });

  test('完整任务快照超过200项时四状态计数不截断，合计等于全部任务且保留每任务空值', async () => {
    const data = fixture();
    const seed = data.tasks[0];
    if (!seed) throw new Error('缺少任务测试数据');
    const states: Overview['tasks'][number]['state'][] = [
      ...Array<Overview['tasks'][number]['state']>(205).fill('monitoring'),
      'checking', 'checking', 'error', 'error', 'error', 'paused', 'paused', 'paused', 'paused', 'paused',
    ];
    data.tasks = states.map((state, index) => ({
      ...seed, task_id: index + 1, name: `完整快照任务${index + 1}`, state, trend: [],
      success_rate: index === 0 ? null : 0, average_duration_ms: index === 0 ? null : 0,
      next_run_at: index === 0 ? null : 0,
    }));
    const requests: Request[] = [];
    openPage(async (request) => { requests.push(request); return Response.json(data); });
    const status = await screen.findByRole('region', { name: '任务状态' });
    const summary = within(status).getAllByRole('definition').slice(0, 4).map((entry) => Number(entry.textContent));
    expect(summary).toEqual([205, 2, 3, 5]);
    expect(summary.reduce((total, count) => total + count, 0)).toBe(215);
    expect(status.textContent).not.toContain('检查汇总暂不可用');
    const unknownTask = within(status).getByRole('link', { name: '完整快照任务1' }).closest('li');
    const zeroTask = within(status).getByRole('link', { name: '完整快照任务2' }).closest('li');
    if (!unknownTask || !zeroTask) throw new Error('缺少每任务状态');
    expect(within(unknownTask).getAllByText('暂不可用')).toHaveLength(3);
    expect(within(zeroTask).queryByText('暂不可用')).toBeNull();
    expect(within(zeroTask).getByText('0%')).toBeTruthy();
    expect(requests).toHaveLength(1);
    expect(new URL(requests[0]!.url).pathname).toBe('/api/v1/overview');
  });

  test('没有任务时保留历史统计和未解决异常，空账号不伪造可用状态', async () => {
    const data = fixture(); data.tasks = []; data.accounts = { total: 0, active: 0 };
    openPage(async () => Response.json(data));
    await screen.findByRole('heading', { level: 2, name: '还没有监控任务' });
    expect(screen.getByRole('region', { name: '扫描商品' }).textContent).toContain('731');
    expect(screen.getByRole('region', { name: '异常' }).textContent).not.toContain('没有未解决的系统事件。');
    expect(screen.getByRole('region', { name: '闲鱼账号' }).textContent).toContain('0 个可用，共 0 个');
    expect(screen.getAllByRole('link', { name: '管理任务' }).length).toBeGreaterThan(0);
    const status = screen.getByRole('region', { name: '任务状态' });
    expect(within(status).getAllByRole('definition').map((entry) => entry.textContent)).toEqual(['0', '0', '0', '0']);
    expect(status.textContent).not.toContain('检查汇总暂不可用');
  });

  test('英文界面完整读取同一协议，任务原名保留原文', async () => {
    openPage(async () => Response.json(fixture()), 'en');
    await screen.findByRole('region', { name: 'Listings scanned' });
    expect(screen.getByRole('heading', { level: 1, name: 'Monitoring overview' })).toBeTruthy();
    expect(screen.getByRole('radio', { name: 'Past 7 days' })).toBeTruthy();
    expect(screen.getByRole('region', { name: 'Task performance' }).textContent).toContain('便携录音机');
    expect(screen.getByRole('region', { name: 'AI calls' }).textContent).toContain('91%');
  });
});
