import { afterEach, describe, expect, mock, test } from 'bun:test';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { act, render, screen, waitFor, within, fireEvent } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState, type ReactNode } from 'react';
import { createApiClient } from '../../api/client';
import { ApiProvider } from '../../api/hooks';
import type { Schema } from '../../api/types';
import { createI18n, I18nProvider } from '../../i18n';
import { ComparePage } from './compare-page';
import { FindingDetail } from './finding-detail';
import { AnalysisWarnings, ContactQuestions, ScoreBreakdown, Seller } from './presentation';
import { Risks } from './editors';
import { profileFields } from './data';

const timestamp = 1_799_971_200_000;
const clients: QueryClient[] = [];
afterEach(() => { act(() => { for (const client of clients.splice(0)) client.clear(); }); });

function fixture(id = 1): Schema<'FindingView'> {
  const profile: Schema<'AnalysisProfileFields'> = {
    summary: '本地测试需求', specs: [{ name: '容量', options: ['256G', '512G'], values: ['256G'], source: 'user', edited: true }],
    musts: [], bonuses: [], risks: [
      { id: 'id_lock', attitude: 'confirm', source: 'user', edited: true, rev: 3 },
      { id: 'water_damage', attitude: 'reject', source: 'user', edited: true, rev: 2 },
    ], allowed_kinds: [], next_no: 1,
  };
  return {
    id, task_id: 5, item_id: `item-${id}`, search_generation: 1, found_at: timestamp, is_baseline: false,
    decision: 'scored', stage: 'done', reject_kind: null, decided_layer: 3, reason: { code: 'analysis.scored', args: { score: 89, grade: 'good' } },
    analysis: { specs: [{ name: '容量', value: '256G' }], brand_new: false, musts: [], bonuses: [], checks: [
      { key: 'id_lock', status: 'unknown', source: null, evidence: null, rev: 3 },
      { key: 'water_damage', status: 'ok', source: 'text', evidence: '没有进过水', rev: 2 },
    ], seller_signals: [], price_note: null, extra_findings: [{ name: '屏幕压伤', evidence: '屏幕有一处压痕', source: 'text' }], adjust: { value: 8, reason: '本地测试判断' }, summary: '本地测试结论' },
    analysis_warnings: [], user_overrides: { risks: { water_damage: { status: 'ok', rev: 2 } } }, score: 89, grade: 'good',
    score_breakdown: { base: { score: 87, d: .23, fair_price: 130000, sample: 6, basis: { code: 'analysis.fair_same_config', args: { days: 14, count: 6 } } }, items: [], ai: { value: 8, reason: '本地测试判断' }, caps: [{ limit: 89, reason: { code: 'analysis.risk_unknown', args: { id: 'id_lock', name: 'ID 锁', attitude: 'confirm' } } }], gates: [] },
    fair_price: 130000, fair_sample: 6, fair_basis: { code: 'analysis.fair_same_config', args: { days: 14, count: 6 } }, scored_price: 100000, scored_at: timestamp,
    summary: '本地测试结论', title_screen_reason: null, analysis_provider_id: 1, analysis_model: 'local-fixture', analyzed_at: timestamp,
    seller_type: 'unknown', matched_keywords: [], attempts: 1, retryable: false, reanalyze_requested_at: null, is_read: true,
    notify_kind: null, notify_skip_reason: null, notified_at: null, recommended_price: null, last_drop_notified_price: null, drop_notified_at: null,
    listing: { item_id: `item-${id}`, title: `本地商品 ${id}`, desktop_url: `https://example.invalid/item/${id}`, first_seen_at: timestamp, last_seen_at: timestamp,
      price: 100000, price_text: '1000', original_price_text: null, thumbnail_url: null, area: null, seller_id: 'seller-1', seller_nick: '本地卖家', description: '没有进过水，屏幕有一处压痕', zhima_level: null, sale_status: null,
      image_urls: [], tags: [], published_at: timestamp - 3_600_000, want_count: null, browse_count: null, seller_registration_days: null, detail_fetched_at: timestamp,
      is_favorite: false, is_dismissed: false, favorited_at: null, last_drop_amount: null, last_drop_at: null },
    task: { id: 5, name: '本地任务', judge_mode: 'ai', profile, budget_min: null, budget_max: 150000 },
    item_specs: { task_id: 5, item_id: `item-${id}`, updated_at: timestamp, kind: 'sale', specs: [{ name: '容量', value: '256G' }], brand_new: false, config_key: '256G', excluded: false, source: 'detail' },
    seller: null, seller_assessment: null, checks: [{ key: 'id_lock', status: 'unknown', source: 'unknown', evidence: null }, { key: 'water_damage', status: 'ok', source: 'user', evidence: null }], market_comparison: null, deliveries: [], questions: [{ key: 'id_lock', question: { code: 'analysis.question.id_lock', args: {} } }], previous_id: null, next_id: 2,
  };
}

function taskFixture(finding: Schema<'FindingView'>): Schema<'Task'> {
  return { id: 5, name: '本地任务', keyword: '本地商品', enabled: true, account_id: null, account_pinned: false, min_price: null, max_price: null, personal_only: true, free_shipping_only: false, publish_window: 'any', region_province: null, region_city: null, region_district: null, max_pages: 1, interval_minutes: 30, crawl_overrides: {}, judge_mode: 'ai', requirement: '本地测试需求', profile: finding.task.profile, budget_min: null, budget_max: 150000, match_keywords: [], exclude_keywords: [], analyze_on_first_run: true, notify_policy: 'good', notify_price_drop: true, channel_ids: [], search_generation: 1, baseline_pending: false, sort_index: 0, created_at: timestamp, updated_at: timestamp, last_run_at: null, last_success_at: null, next_run_at: null, last_run_outcome: null, last_error: null, consecutive_failures: 0, failure_paused_until: null, no_good_hint_dismissed_at: null };
}

function json(value: unknown, status = 200) { return new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } }); }
function fakeAPI(initial = fixture(), override?: (request: Request) => Promise<Response | undefined>) {
  let current = initial;
  const calls: { method: string; path: string; search: string; body: unknown }[] = [];
  const api = createApiClient({ fetch: async (request) => {
    const url = new URL(request.url);
    const body: unknown = request.method === 'GET' ? null : await request.clone().json().catch(() => null);
    calls.push({ method: request.method, path: url.pathname, search: url.search, body });
    const replacement = await override?.(request);
    if (replacement) return replacement;
    if (url.pathname === '/api/v1/settings') return json({ values: { 'general.timezone': 'UTC' }, sources: {}, warnings: [] });
    if (url.pathname === '/api/v1/risk-dictionary') return json([
      { id: 'id_lock', name: 'ID 锁', category: '锁和账号', default_attitude: 'confirm', hint: '测试说明', question: '测试问句' },
      { id: 'water_damage', name: '进水', category: '状态', default_attitude: 'reject', hint: '测试说明', question: '测试问句' },
    ]);
    if (url.pathname.endsWith('/prices')) return json({ items: [], next_cursor: null, total: 0 });
    if (url.pathname.endsWith('/market/samples')) return json({ items: [], next_cursor: null, total: 0 });
    if (url.pathname === `/api/v1/findings/${current.id}` && request.method === 'GET') return json(current);
    if (url.pathname === `/api/v1/findings/${current.id}` && request.method === 'PATCH') { current = { ...current, is_read: true }; return json(current); }
    throw new Error(`未声明的本地请求：${request.method} ${url.pathname}`);
  } });
  return { api, calls, get current() { return current; }, update(value: Schema<'FindingView'>) { current = value; } };
}

function mount(content: ReactNode, api = fakeAPI().api) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } } });
  clients.push(client);
  const root = createRootRoute({ component: () => content });
  const router = createRouter({ routeTree: root, history: createMemoryHistory({ initialEntries: ['/'] }) });
  return render(<I18nProvider instance={createI18n('zh-CN')}><ApiProvider api={api}><QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider></ApiProvider></I18nProvider>);
}

describe('商品详情的接口与状态', () => {
  test('详情保留旧版消息的条目与证据链接，复制问句沿用显示原文', async () => {
    const initial = fixture();
    initial.reason = { code: 'legacy.detail.reason', args: {} };
    initial.fair_basis = { code: 'analysis.fair_same_config', args: { count: 14 } };
    if (!initial.score_breakdown) throw new Error('测试商品缺少打分构成');
    initial.score_breakdown.items = [{ id: 'legacy-adjustment', name: '旧版规则', reason: { code: 'legacy.detail.adjustment', args: {} }, source: 'rule', value: -3 }];
    initial.score_breakdown.gates = [{ code: 'legacy.detail.gate', args: {} }];
    initial.score_breakdown.caps.push({ limit: 90, reason: { code: 'legacy.detail.cap', args: {} } });
    initial.analysis_warnings = [{ code: 'analysis.evidence_missing', args: { field: 'id_lock' } }];
    initial.seller_assessment = { type: 'unknown', bonus_eligible: false, checks: [], reasons: [{ code: 'legacy.detail.seller', args: {} }] };
    initial.deliveries = [{ id: 1, at: timestamp, channel_id: 1, channel_name: '本地渠道', task_id: 5, kind: 'price_drop', item_count: 1, ok: false, error: { code: 'legacy.detail.delivery', args: {} } }];
    initial.questions = [
      { key: 'normal', question: { code: 'analysis.question_custom', args: { question: '这条问句保留原文' } } },
      { key: 'legacy', question: { code: 'legacy.detail.question', args: {} } },
      { key: 'incomplete', question: { code: 'analysis.question_custom', args: {} } },
    ];
    const user = userEvent.setup();
    const write = mock(async (_text: string) => {});
    const previous = navigator.clipboard;
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: write } });
    try {
      mount(<FindingDetail findingId={1} onClose={() => {}} onNavigate={() => {}} timeZone="UTC" />, fakeAPI(initial).api);
      await screen.findByRole('heading', { name: '本地商品 1' });
      for (const code of ['legacy.detail.reason', 'analysis.fair_same_config', 'legacy.detail.adjustment', 'legacy.detail.gate', 'legacy.detail.cap', 'legacy.detail.seller', 'legacy.detail.question', 'analysis.question_custom']) {
        expect(screen.getByText(code).parentElement?.textContent).toContain('旧版本消息');
      }
      const warning = screen.getByRole('link', { name: /analysis.evidence_missing/ });
      expect(warning.getAttribute('href')).toBe('#finding-1-risk-id_lock');
      expect(warning.textContent).toContain('旧版本消息');
      const history = screen.getByRole('heading', { name: '价格走势' }).closest('section')!;
      expect(within(history).getByText('legacy.detail.delivery').parentElement?.textContent).toContain('旧版本消息');
      expect(screen.getByText('这条问句保留原文')).toBeTruthy();
      await user.click(screen.getByRole('button', { name: '复制这几句' }));
      expect(write).toHaveBeenCalledWith('这条问句保留原文\nlegacy.detail.question\nanalysis.question_custom');
    } finally { Object.defineProperty(navigator, 'clipboard', { configurable: true, value: previous }); }
  });

  test('纯GET详情之后显式标已读，保持八块顺序和后台封顶分数', async () => {
    const initial = fixture(); initial.is_read = false;
    const backend = fakeAPI(initial);
    const navigate = mock(() => {});
    mount(<FindingDetail findingId={1} query={{ task_id: 5, view: 'other', sort: 'latest' }} onClose={() => {}} onNavigate={navigate} timeZone="UTC" />, backend.api);
    await screen.findByRole('heading', { name: '本地商品 1' });
    await waitFor(() => expect(backend.calls.some((call) => call.method === 'PATCH' && call.path === '/api/v1/findings/1')).toBe(true));
    const detailIndex = backend.calls.findIndex((call) => call.path === '/api/v1/findings/1' && call.method === 'GET');
    const readIndex = backend.calls.findIndex((call) => call.path === '/api/v1/findings/1' && call.method === 'PATCH');
    expect(detailIndex).toBeLessThan(readIndex);
    expect(backend.calls[detailIndex]?.search).toContain('view=other');
    expect(backend.calls[readIndex]?.body).toEqual({ is_read: true });
    expect(screen.getByText('89', { selector: 'strong' })).toBeTruthy();
    expect(screen.queryByText('95', { selector: 'strong' })).toBeNull();
    const headings = screen.getAllByRole('heading', { level: 2 }).map((node) => node.textContent);
    expect(headings).toEqual(['分数怎么来的', '联系前问这几句', '风险清单', '卖家画像', '同配置对比', '价格走势', '一句话结论']);
    await userEvent.setup().click(screen.getByRole('button', { name: '下一个商品' }));
    expect(navigate).toHaveBeenCalledWith(2, 5);
  });


  test('标为未读失败后重试仍发送false，不反向标成已读', async () => {
    let attempts = 0;
    const backend = fakeAPI(fixture(), async (request) => {
      if (request.method === 'PATCH' && new URL(request.url).pathname === '/api/v1/findings/1') {
        attempts++;
        if (attempts === 1) return json({ error: { code: 'web.request_failed', args: {} }, fields: [], request_id: 'local-read-failure' }, 500);
        backend.update({ ...backend.current, is_read: false });
        return json(backend.current);
      }
      return undefined;
    });
    mount(<FindingDetail findingId={1} onClose={() => {}} onNavigate={() => {}} timeZone="UTC" />, backend.api);
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '标为未读' }));
    await user.click(await screen.findByRole('button', { name: '重试' }));
    await waitFor(() => expect(attempts).toBe(2));
    const writes = backend.calls.filter((call) => call.method === 'PATCH' && call.path === '/api/v1/findings/1');
    expect(writes.map((call) => call.body)).toEqual([{ is_read: false }, { is_read: false }]);
    await screen.findByText('已保存');
    expect(backend.current.is_read).toBe(false);
  });

  test('切换商品会取消旧读取，旧响应不能覆盖新商品', async () => {
    let firstSignal: AbortSignal | undefined;
    const backend = fakeAPI(fixture(), async (request) => {
      const path = new URL(request.url).pathname;
      if (path === '/api/v1/findings/1' && request.method === 'GET') {
        firstSignal = request.signal;
        return await new Promise<Response>((_resolve, reject) => request.signal.addEventListener('abort', () => reject(new DOMException('cancelled', 'AbortError')), { once: true }));
      }
      if (path === '/api/v1/findings/2') return json(fixture(2));
      return undefined;
    });
    function Example() {
      const [id, setID] = useState(1);
      return <><button onClick={() => setID(2)}>换一件</button><FindingDetail findingId={id} onClose={() => {}} onNavigate={() => {}} timeZone="UTC" /></>;
    }
    mount(<Example />, backend.api);
    await waitFor(() => expect(firstSignal).toBeDefined());
    await userEvent.setup().click(screen.getByRole('button', { name: '换一件' }));
    await screen.findByRole('heading', { name: '本地商品 2' });
    expect(firstSignal?.aborted).toBe(true);
    expect(screen.queryByRole('heading', { name: '本地商品 1' })).toBeNull();
  });

  test('修改一条结论保留其它覆盖和rev，422失败保留选择，再试后读取后台结果', async () => {
    let attempts = 0;
    const backend = fakeAPI(fixture(), async (request) => {
      if (new URL(request.url).pathname.endsWith('/overrides')) {
        attempts++;
        if (attempts === 1) return json({ error: { code: 'web.request_failed', args: {} }, fields: [], request_id: 'local-request-1' }, 422);
        return json(backend.current);
      }
      return undefined;
    });
    mount(<FindingDetail findingId={1} onClose={() => {}} onNavigate={() => {}} timeZone="UTC" />, backend.api);
    const trigger = await screen.findByRole('button', { name: '修改“ID 锁”的结论' });
    const user = userEvent.setup();
    await user.click(trigger);
    const dialog = screen.getByRole('dialog', { name: '修改“ID 锁”的结论' });
    await user.click(within(dialog).getByRole('radio', { name: '没问题' }));
    await user.click(within(dialog).getByRole('button', { name: '保存' }));
    await within(dialog).findByText('local-request-1');
    expect(within(dialog).getByRole('radio', { name: '没问题' }).getAttribute('aria-checked')).toBe('true');
    expect(backend.calls.find((call) => call.path.endsWith('/overrides'))?.body).toEqual({ risks: { water_damage: { status: 'ok', rev: 2 }, id_lock: { status: 'ok', rev: 3 } } });
    expect(attempts).toBe(1);
    await user.click(within(dialog).getByRole('button', { name: '保存' }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '修改“ID 锁”的结论' })).toBeNull());
    expect(attempts).toBe(2);
  });

  test('规格保存明确null与新旧状态，不写source或分数', async () => {
    const backend = fakeAPI(fixture(), async (request) => {
      if (new URL(request.url).pathname.endsWith('/specs')) {
        const result: Schema<'ItemSpecsUpdateResult'> = { item_specs: { ...fixture().item_specs!, source: 'user' }, rescored_count: 1 };
        return json(result);
      }
      return undefined;
    });
    mount(<FindingDetail findingId={1} onClose={() => {}} onNavigate={() => {}} timeZone="UTC" />, backend.api);
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '修改这件商品的规格' }));
    const dialog = screen.getByRole('dialog', { name: '修改这件商品的规格' });
    fireEvent.change(within(dialog).getByRole('combobox', { name: '容量' }), { target: { value: '' } });
    await user.click(within(dialog).getByRole('button', { name: '保存' }));
    await waitFor(() => expect(backend.calls.some((call) => call.path.endsWith('/specs'))).toBe(true));
    expect(backend.calls.find((call) => call.path.endsWith('/specs'))?.body).toEqual({ specs: [{ name: '容量', value: null }], brand_new: false });
  });

  test('加进清单发送完整原发现，反馈后台数量，不自动重分析其它商品', async () => {
    const backend = fakeAPI(fixture(), async (request) => {
      if (new URL(request.url).pathname.endsWith('/extra-check')) {
        const result: Schema<'TaskSaveResult'> = { task: taskFixture(fixture()), effects: { search_reset: false, generation: 1, rescored_count: 1, needs_reanalysis_count: 7, title_rejected_count: 0, needs_reanalysis_ids: [], title_rejected_ids: [], added_exclude_words: [], existing_exclude_match_count: 0 }, warnings: [] };
        return json(result);
      }
      return undefined;
    });
    mount(<FindingDetail findingId={1} onClose={() => {}} onNavigate={() => {}} timeZone="UTC" />, backend.api);
    await userEvent.setup().click(await screen.findByRole('button', { name: '加进这个任务的清单' }));
    await screen.findByText('已有 7 个商品要重新分析才能判断这一条');
    expect(backend.calls.find((call) => call.path.endsWith('/extra-check'))?.body).toEqual({ extra_finding: { name: '屏幕压伤', evidence: '屏幕有一处压痕', source: 'text' } });
    expect(backend.calls.some((call) => call.path.includes('reanaly') || call.path.endsWith('/run'))).toBe(false);
  });

  test('重分析先预览再明确提交，202排队不显示为完成', async () => {
    const backend = fakeAPI(fixture(), async (request) => {
      const path = new URL(request.url).pathname;
      if (path.endsWith('/reanalysis-preview')) return json({ as_of: timestamp, requested_count: 1, needs_detail_count: 1, cached_detail_count: 0 });
      if (path.endsWith('/reanalyze')) {
        backend.update({ ...backend.current, decision: 'pending', score: null, grade: null, score_breakdown: null });
        return json({ items: [{ finding_id: 1, status: 'queued', reason: null, needs_detail: true }], needs_detail_count: 1 }, 202);
      }
      return undefined;
    });
    mount(<FindingDetail findingId={1} onClose={() => {}} onNavigate={() => {}} timeZone="UTC" />, backend.api);
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '重新分析' }));
    const dialog = await screen.findByRole('alertdialog', { name: '重新分析' });
    expect(backend.calls.some((call) => call.path.endsWith('/reanalyze'))).toBe(false);
    await user.click(within(dialog).getByRole('button', { name: '重新分析' }));
    await screen.findByText('已排队，将由之后的运行处理');
    await waitFor(() => expect(screen.queryByText('89', { selector: 'strong' })).toBeNull());
    expect(screen.queryByText('本地测试结论')).toBeNull();
  });

  test('当前行情与上次分数依据分开，邻配置差价只展示后台值和真实采样参数', async () => {
    const initial = fixture();
    initial.market_comparison = { as_of: timestamp + 10000, config_key: '256G', brand_new: false,
      fair: { price: 141200, sample: 5, basis: { code: 'analysis.fair_same_config', args: { days: 30, count: 5 } }, days: 30, dropped_spec: null, item_ids: ['sample-1'] },
      adjacent_prices: [{ spec_name: '容量', from: '128G', to: '256G', lower_config_key: '128G', item_premium: -10000, typical_premium: 60000 }] };
    const backend = fakeAPI(initial, async (request) => new URL(request.url).pathname.endsWith('/market/samples')
      ? json({ items: [{ item_id: 'sample-1', title: '当前样本', price: 141200, seller_id: null, observed_at: timestamp, config_key: '256G', brand_new: false, desktop_url: 'https://example.invalid/sample' }], next_cursor: null, total: 1 }) : undefined);
    mount(<FindingDetail findingId={1} onClose={() => {}} onNavigate={() => {}} timeZone="UTC" />, backend.api);
    await screen.findByText('当前样本');
    expect(screen.getByText('¥1,300.00')).toBeTruthy();
    expect(screen.getAllByText('¥1,412.00').length).toBe(2);
    expect(screen.getByText(/这件差价-¥100.00/)).toBeTruthy();
    const request = backend.calls.find((call) => call.path.endsWith('/market/samples'));
    expect(request?.search).toContain('config_key=256G');
    expect(request?.search).toContain('brand_new=false');
  });

  test('价格历史不把首个分页当全部，继续按游标读剩余价格', async () => {
    const backend = fakeAPI(fixture(), async (request) => {
      const url = new URL(request.url);
      if (url.pathname.endsWith('/prices')) return json(url.searchParams.has('cursor')
        ? { items: [{ id: 2, item_id: 'item-1', price: 90000, observed_at: timestamp + 1000 }], next_cursor: null, total: 2 }
        : { items: [{ id: 1, item_id: 'item-1', price: 100000, observed_at: timestamp }], next_cursor: 'page-two', total: 2 });
      return undefined;
    });
    mount(<FindingDetail findingId={1} onClose={() => {}} onNavigate={() => {}} timeZone="UTC" />, backend.api);
    await userEvent.setup().click(await screen.findByRole('button', { name: '加载更多' }));
    await waitFor(() => expect(backend.calls.some((call) => call.path.endsWith('/prices') && call.search.includes('cursor=page-two'))).toBe(true));
    expect(await screen.findByText('¥900.00')).toBeTruthy();
  });
});

describe('比较和局部阅读', () => {
  test('并排对比在各自列保留旧版原因和评分依据，也保留正常翻译', async () => {
    const first = fixture();
    first.reason = { code: 'legacy.compare.reason', args: {} };
    first.fair_basis = { code: 'analysis.fair_same_config', args: { days: 14 } };
    first.seller_assessment = { type: 'unknown', bonus_eligible: false, checks: [], reasons: [{ code: 'legacy.compare.seller', args: {} }] };
    const second = fixture(2);
    const backend = fakeAPI(first, async (request) => new URL(request.url).pathname === '/api/v1/findings/2' ? json(second) : undefined);
    mount(<ComparePage ids={[1, 2]} timeZone="UTC" />, backend.api);
    await screen.findByRole('heading', { name: '本地商品 2' });
    const table = within(screen.getByRole('region', { name: '并排对比' }));
    const oldReason = table.getByText('legacy.compare.reason');
    expect(oldReason.parentElement?.textContent).toContain('旧版本消息');
    expect(oldReason.closest('td')?.cellIndex).toBe(1);
    expect(table.getByText('值得分 89').closest('td')?.cellIndex).toBe(2);
    const historicalBases = table.getAllByText('analysis.fair_same_config');
    expect(historicalBases).toHaveLength(2);
    for (const basis of historicalBases) expect(basis.parentElement?.textContent).toContain('旧版本消息');
    expect(table.getAllByText('同配置 · 近 14 天')).toHaveLength(2);
    expect(table.getByText('legacy.compare.seller').parentElement?.textContent).toContain('旧版本消息');
  });

  test('越过2–4件边界不给后台偷偷截断或发请求', async () => {
    const backend = fakeAPI();
    mount(<ComparePage ids={[1, 2, 3, 4, 5]} timeZone="UTC" />, backend.api);
    await screen.findByRole('heading', { name: '请选择 2 到 4 件商品对比' });
    expect(backend.calls.some((call) => call.path.includes('/findings'))).toBe(false);
  });

  test('两件比较保留null、上下文和局部横滑，不标已读、不重新计算得分', async () => {
    const second = { ...fixture(2), score: null, grade: null, fair_price: null, score_breakdown: null } satisfies Schema<'FindingView'>;
    second.listing.price = null;
    const backend = fakeAPI(fixture(), async (request) => new URL(request.url).pathname === '/api/v1/findings/2' ? json(second) : undefined);
    mount(<ComparePage ids={[1, 2]} timeZone="UTC" />, backend.api);
    await screen.findByRole('heading', { name: '本地商品 2' });
    const region = screen.getByRole('region', { name: '并排对比' });
    expect(region.className).toContain('overflow-x-auto');
    expect(within(region).getAllByText('价格未知').length).toBeGreaterThan(0);
    expect(within(region).getAllByText('无法比价').length).toBeGreaterThan(0);
    expect(backend.calls.some((call) => call.method !== 'GET')).toBe(false);
  });

  test('最多复制后台有序问句的前五句，不自动给卖家发消息', async () => {
    const user = userEvent.setup();
    const write = mock(async (_text: string) => {});
    const previous = navigator.clipboard;
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: write } });
    try {
      const questions: Schema<'AnalysisContactQuestion'>[] = Array.from({ length: 7 }, (_, index) => ({ key: `c${index}`, question: { code: 'analysis.question_custom', args: { question: `本地问句${index + 1}` } } }));
      mount(<ContactQuestions questions={questions} />);
      await user.click(await screen.findByRole('button', { name: '复制这几句' }));
      expect(write).toHaveBeenCalledWith('本地问句1\n本地问句2\n本地问句3\n本地问句4\n本地问句5');
      expect(screen.queryByText('本地问句6')).toBeNull();
    } finally { Object.defineProperty(navigator, 'clipboard', { configurable: true, value: previous }); }
  });

  test('原文核对警告仅取程序字段，按稳定编号定位，不从疑似结论猜警告', async () => {
    const finding = fixture();
    finding.checks[0] = { key: 'id_lock', status: 'suspect', source: 'unknown', evidence: null };
    function Example() {
      const [show, setShow] = useState(false);
      return <><button onClick={() => setShow(true)}>展示程序警告</button><AnalysisWarnings finding={{ ...finding, analysis_warnings: show ? [{ code: 'analysis.evidence_missing', args: { item: 'id_lock' } }] : [] }} linkChecks /></>;
    }
    mount(<Example />);
    await screen.findByRole('button', { name: '展示程序警告' });
    expect(screen.queryByText(/引用的原文没找到/)).toBeNull();
    await userEvent.setup().click(screen.getByRole('button', { name: '展示程序警告' }));
    expect(screen.getByRole('link', { name: /引用的原文没找到/ }).getAttribute('href')).toBe('#finding-1-risk-id_lock');
  });

  test('原始卖家缓存清理后仍展示分析时的程序依据，不把未知卖家当个人', async () => {
    const finding = fixture();
    finding.seller = null;
    finding.seller_assessment = { type: 'dealer_suspect', bonus_eligible: false, checks: [{ key: 'seller_dealer', status: 'risk', source: 'program', evidence: '同类在售 4 件' }], reasons: [{ code: 'analysis.seller_dealer_items', args: { count: 4 } }] };
    mount(<Seller finding={finding} timeZone="UTC" />);
    await screen.findByText('疑似商家');
    expect(screen.getByText('最近在售商品有 4 件同类商品')).toBeTruthy();
    expect(screen.queryByText('个人')).toBeNull();
    expect(screen.getByText('卖家原始资料缓存已不可用，分析时的判断依据仍保留')).toBeTruthy();
  });

  test('只取得部分卖家资料时，详情保留已取得的字段并把评价显示为未知', async () => {
    const finding = fixture();
    finding.seller = {
      seller_id: 'seller-1', nick: '部分资料的卖家', avatar_url: null, introduction: '保留卖家介绍', seller_credit: null, buyer_credit: null,
      seller_credit_level: null, buyer_credit_level: null, item_count: null, rate_count: null, on_sale_count: null, sold_count: null,
      seller_good_rate: null, buyer_good_rate: null, seller_good_count: null, seller_rate_count: null, buyer_good_count: null, buyer_rate_count: null,
      recent_items: null, recent_rates: null, fetched_at: timestamp,
    };
    mount(<FindingDetail findingId={1} onClose={() => {}} onNavigate={() => {}} timeZone="UTC" />, fakeAPI(finding).api);
    await screen.findByRole('heading', { name: '本地商品 1' });
    expect(screen.getByText('部分资料的卖家')).toBeTruthy();
    expect(screen.getByText('保留卖家介绍')).toBeTruthy();
    const reviews = screen.getByRole('heading', { name: '最近评价' }).parentElement!;
    expect(within(reviews).getByText('未知')).toBeTruthy();
  });

  test('当前有效结论直接用后台checks，旧覆盖不能把程序卖家风险显示成没问题', async () => {
    const finding = fixture();
    const profile = profileFields(finding.task.profile);
    if (!profile) throw new Error('本地清单缺失');
    profile.risks.push({ id: 'seller_new', attitude: 'mind', source: 'system', edited: false, rev: 1 });
    finding.user_overrides.risks.seller_new = { status: 'ok', rev: 0 };
    finding.checks.push({ key: 'seller_new', status: 'risk', source: 'program', evidence: '注册仅 10 天' });
    const dictionary: Schema<'AnalysisRiskDefinition'>[] = [
      { id: 'seller_new', name: '新号', category: '卖家', default_attitude: 'mind', hint: '程序判断', question: '' },
    ];
    mount(<Risks finding={finding} dictionary={dictionary} />);
    await screen.findByText('注册仅 10 天');
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '修改“新号”的结论' }));
    const dialog = screen.getByRole('dialog', { name: '修改“新号”的结论' });
    expect(within(dialog).getByRole('radio', { name: '有风险' }).getAttribute('aria-checked')).toBe('true');
    expect(within(dialog).getByRole('radio', { name: '没问题' }).getAttribute('aria-checked')).toBe('false');
  });

  test('重新分析失败不展示旧分数和旧构成', async () => {
    const data = fixture(); data.decision = 'failed'; data.score = null; data.grade = null;
    mount(<ScoreBreakdown finding={data} />);
    await screen.findByText('分析失败');
    expect(screen.queryByText('89', { selector: 'strong' })).toBeNull();
    expect(screen.queryByText('基础分')).toBeNull();
  });
});
