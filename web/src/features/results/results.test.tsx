import { afterEach, describe, expect, spyOn, test } from 'bun:test';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from '@tanstack/react-router';
import { useState } from 'react';
import { createApiClient, type ApiFetch } from '../../api/client';
import * as downloadAPI from '../../api/download';
import { ApiProvider } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import type { Schema } from '../../api/types';
import type { ResultsSearch } from '../../app/search';
import { createAppQueryClient } from '../../app/query-client';
import { createI18n, I18nProvider, type Locale } from '../../i18n';
import { registrationAge } from '../../i18n/format';
import { ResultsPage } from './page';
import { ResultsFilters } from './filters';
import { NotificationStatus } from './notification-status';
import { ListingPanel, SavedListingCard } from './listing-panel';
import { FindingCard } from './finding-card';
import { detailQuery, effectiveRisks, findingQuery, isOverBudget, latestDeliveries, listingQuery, resultTime, safeExternalURL, selectableIDs, type Finding } from './model';

const clients: ReturnType<typeof createAppQueryClient>[] = [];
const restoreDownloadSpies: Array<() => void> = [];
afterEach(() => {
  cleanup();
  for (const client of clients.splice(0)) client.clear();
  for (const restore of restoreDownloadSpies.splice(0)) restore();
});

function observeDownloads() {
  const download = spyOn(downloadAPI, 'downloadBlob').mockImplementation(() => {});
  restoreDownloadSpies.push(() => download.mockRestore());
  return download;
}

function listing(patch: Partial<Schema<'Listing'>> = {}): Schema<'Listing'> {
  return {
    item_id: '8001', title: '自用相机机身', desktop_url: 'https://example.invalid/item/8001',
    first_seen_at: 1794355200000, last_seen_at: 1794355200000, price: 180000,
    price_text: null, original_price_text: null, thumbnail_url: null, area: null, seller_id: null, seller_nick: null,
    description: null, zhima_level: null, sale_status: null, image_urls: [], tags: [], published_at: null,
    want_count: null, browse_count: null, seller_registration_days: null, detail_fetched_at: null,
    is_favorite: false, is_dismissed: false, favorited_at: null, last_drop_amount: null, last_drop_at: null, ...patch,
  };
}

function finding(patch: Partial<Finding> = {}): Finding {
  return {
    id: 31, task_id: 7, item_id: '8001', search_generation: 1, found_at: 1794355200000, is_baseline: false,
    decision: 'scored', stage: 'done', reject_kind: null, decided_layer: 3, reason: null, analysis: null, analysis_warnings: [],
    user_overrides: { risks: {} }, score: 84, grade: 'good', score_breakdown: null, fair_price: 220000,
    fair_sample: 12, fair_basis: null, scored_price: 180000, scored_at: 1794355200000,
    summary: null, title_screen_reason: null, analysis_provider_id: null, analysis_model: null, analyzed_at: null,
    seller_type: 'unknown', matched_keywords: [], attempts: 1, retryable: false, reanalyze_requested_at: null,
    is_read: false, notify_kind: null, notify_skip_reason: null, notified_at: null, recommended_price: null,
    last_drop_notified_price: null, drop_notified_at: null, listing: listing(),
    task: { id: 7, name: '相机', judge_mode: 'keyword', profile: {}, budget_min: null, budget_max: null },
    item_specs: null, seller: null, seller_assessment: null, market_comparison: null, checks: [], deliveries: [], questions: [], previous_id: null, next_id: null, ...patch,
  };
}

function task(): Schema<'TaskView'> {
  return {
    id: 7, name: '相机', keyword: '相机', enabled: true, account_id: null, account_pinned: false,
    min_price: null, max_price: null, personal_only: true, free_shipping_only: false, publish_window: 'any',
    region_province: null, region_city: null, region_district: null, max_pages: 1, interval_minutes: 30,
    crawl_overrides: {}, judge_mode: 'keyword', requirement: '', profile: {}, budget_min: null, budget_max: null,
    match_keywords: [], exclude_keywords: [], analyze_on_first_run: true, notify_policy: 'recommended',
    notify_price_drop: true, channel_ids: [], search_generation: 1, baseline_pending: false, sort_index: 0,
    created_at: 1794355200000, updated_at: 1794355200000, last_run_at: null, last_success_at: null,
    next_run_at: null, last_run_outcome: null, last_error: null, consecutive_failures: 0,
    failure_paused_until: null, no_good_hint_dismissed_at: null,
    runtime: { task_id: 7, phase: 'idle', run_id: null, account_id: null, queued_at: null, started_at: null, blocked: null },
    finding_counts: {}, no_good_hint: null,
  };
}

const json = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } });
function base(request: Request): Response | undefined {
  const url = new URL(request.url);
  if (url.pathname === '/api/v1/tasks') return json({ items: [task()], next_cursor: null, total: 1 } satisfies Schema<'PageTaskView'>);
  if (url.pathname === '/api/v1/tasks/7') return json(task());
  if (url.pathname === '/api/v1/tasks/7/market') return json({ market: { snapshot_at: null, groups: [], task_trend: [], warnings: [] }, insight: null, today_new_count: 0, today_recommended_count: 0, personal_seller_share: null } satisfies Schema<'MarketView'>);
  if (url.pathname === '/api/v1/settings') return json({ values: { 'general.timezone': 'Asia/Singapore' }, sources: {}, warnings: [] } satisfies Schema<'SettingsSnapshot'>);
  return undefined;
}

async function page(fetch: ApiFetch, initialSearch: ResultsSearch = {}, locale: Locale = 'zh-CN') {
  const queryClient = createAppQueryClient();
  clients.push(queryClient);
  function Content() {
    const [search, setSearch] = useState(initialSearch);
    return <ResultsPage search={search} onSearchChange={setSearch} />;
  }
  const root = createRootRoute({ component: Outlet });
  const resultsRoute = createRoute({ getParentRoute: () => root, path: '/results', component: Content });
  const tasksRoute = createRoute({ getParentRoute: () => root, path: '/tasks', component: () => null });
  const compareRoute = createRoute({ getParentRoute: () => root, path: '/results/compare', component: () => null });
  const router = createRouter({ routeTree: root.addChildren([resultsRoute, tasksRoute, compareRoute]), history: createMemoryHistory({ initialEntries: ['/results'] }) });
  await router.load();
  render(<QueryClientProvider client={queryClient}><I18nProvider instance={createI18n(locale)}><ApiProvider api={createApiClient({ fetch })}><RouterProvider router={router} /></ApiProvider></I18nProvider></QueryClientProvider>);
  return router;
}

const allFailedSearch: ResultsSearch = { task_id: 7, view: 'all', decision: 'failed', q: '机身', price_drop: true, over_budget: true, sort: 'latest', cursor: 'screen-page' };
function failedItems() {
  return Array.from({ length: 215 }, (_, index) => finding({ id: 31 + index, item_id: String(8001 + index), decision: 'failed', grade: null, score: null }));
}
function failedPage(request: Request, items: Finding[]): Response {
  const url = new URL(request.url);
  const collecting = url.searchParams.get('limit') === '200';
  const second = url.searchParams.get('cursor') === 'failed-next';
  return json({ items: collecting ? second ? [...items.slice(200), ...items.slice(0, 1)] : items.slice(0, 200) : items.slice(0, 1), next_cursor: second ? null : 'failed-next', total: items.length } satisfies Schema<'PageFindingView'>);
}
function batchPreview(ids: number[]): Response {
  const detail = ids.length === 15 ? 15 : 0;
  return json({ as_of: 1794355200000, requested_count: ids.length, needs_detail_count: detail, cached_detail_count: ids.length - detail } satisfies Schema<'ReanalysisPreview'>);
}
function batchReanalysis(ids: number[]): Response {
  return json({ items: ids.map((finding_id) => ({ finding_id, status: 'queued', reason: null, needs_detail: true })), needs_detail_count: ids.length } satisfies Schema<'ReanalysisResult'>, 202);
}

const csvPath = '/api/v1/tasks/7/findings/export';
const csvHeader = '\uFEFF标题,价格,配置,合理价,分数,档位,结论,主要风险,理由,卖家,地区,发现时间,链接\r\n';
const csvMedia = 'text/csv; charset=utf-8';

describe('商品发布时间', () => {
  for (const locale of ['zh-CN', 'en', 'ja'] as const) {
    test.each([null, 1794355200000])(`${locale} 卡片和独立详情的发布时间 %j 只显示一次标签`, async (publishedAt) => {
      const saved = { ...listing({ published_at: publishedAt }), findings: [], prices: [], seller: null } satisfies Schema<'ListingView'>;
      const api = createApiClient({ fetch: async (request) => {
        const common = base(request); if (common) return common;
        return new URL(request.url).pathname.endsWith('/prices') ? json({ items: [], next_cursor: null, total: 0 }) : json(saved);
      } });
      const client = createAppQueryClient();
      clients.push(client);
      const i18n = createI18n(locale);
      render(<QueryClientProvider client={client}><I18nProvider instance={i18n}><ApiProvider api={api}>
        <FindingCard finding={finding({ listing: saved })} selected={false} selectionFull={false} busy={false} onSelect={() => {}} onOpen={() => {}} onAction={() => {}} timeZone="Asia/Singapore" />
        <ListingPanel itemId={saved.item_id} />
      </ApiProvider></I18nProvider></QueryClientProvider>);
      await screen.findByRole('heading', { name: saved.title });
      const expected = publishedAt === null ? i18n.t('results.published_unknown')
        : i18n.t('results.card.published', { time: resultTime(publishedAt, locale, 'Asia/Singapore') });
      expect(screen.getAllByText(expected, { exact: true })).toHaveLength(2);
      if (publishedAt === null) {
        const repeated = i18n.t('results.card.published', { time: expected });
        expect(screen.queryByText(repeated, { exact: true })).toBeNull();
      }
    });
  }
});

describe('卖家昵称缺失的展示', () => {
  for (const locale of ['zh-CN', 'en'] as const) {
    test.each([null, '', ' \t', '真实卖家'])(`${locale} 收藏卡片和独立详情的昵称 %j 不依赖卖家编号`, async (nick) => {
      const saved = { ...listing({ seller_nick: nick, seller_id: null, is_favorite: true }), findings: [], prices: [], seller: null } satisfies Schema<'ListingView'>;
      const api = createApiClient({ fetch: async (request) => {
        const common = base(request); if (common) return common;
        return new URL(request.url).pathname.endsWith('/prices') ? json({ items: [], next_cursor: null, total: 0 }) : json(saved);
      } });
      const client = createAppQueryClient();
      clients.push(client);
      render(<QueryClientProvider client={client}><I18nProvider instance={createI18n(locale)}><ApiProvider api={api}>
        <SavedListingCard listing={saved} onOpen={() => {}} /><ListingPanel itemId={saved.item_id} />
      </ApiProvider></I18nProvider></QueryClientProvider>);
      await screen.findByRole('heading', { name: saved.title });
      const label = nick?.trim() ? nick : locale === 'zh-CN' ? '未取得卖家昵称' : 'Seller name unavailable';
      expect(screen.getAllByText(label)).toHaveLength(2);
      expect(saved.seller_nick).toBe(nick);
    });
  }

  test.each([null, ''])('屏蔽确认回退昵称 %j 只用于展示，不提交成昵称', async (nick) => {
    const item = finding({ listing: listing({ seller_id: 'seller-1', seller_nick: nick }) });
    const bodies: unknown[] = [];
    await page(async (request) => {
      const common = base(request); if (common) return common;
      if (new URL(request.url).pathname === '/api/v1/blocked-sellers/seller-1') {
        bodies.push(await request.json());
        return json({ seller_id: 'seller-1', nick, created_at: null });
      }
      return json({ items: [item], next_cursor: null, total: 1 });
    });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '更多操作' }));
    await user.click(await screen.findByRole('menuitem', { name: '屏蔽卖家' }));
    const dialog = await screen.findByRole('alertdialog');
    expect(dialog.textContent).toContain('未取得卖家昵称');
    await user.click(within(dialog).getByRole('button', { name: '确认' }));
    await waitFor(() => expect(bodies).toEqual([{ nick }]));
  });
});

describe('任务结果 CSV', () => {
  test.each([215, 0])('导出 %i 条结果使用当前任务和筛选排序，原 Blob 直接交浏览器', async (count) => {
    const download = observeDownloads();
    const rows = Array.from({ length: count }, (_, i) => [`商品 ${i + 1}`, '1800.00', '', '', '', '', '待处理', '', '', '', '', '2026-11-11T00:00:00Z', `https://example.invalid/${i + 1}`].join(',') + '\r\n');
    const blob = new Blob([csvHeader, ...rows], { type: csvMedia });
    const disposition = "attachment; filename*=UTF-8''%E7%9B%B8%E6%9C%BA-2026-11-11.csv";
    const response = new Response(blob, { headers: { 'Content-Type': csvMedia, 'Content-Length': String(blob.size), 'Content-Disposition': disposition } });
    const readBlob = spyOn(response, 'blob').mockResolvedValue(blob);
    const exports: Request[] = [];
    const lists: URL[] = [];
    await page(async (request) => {
      const common = base(request); if (common) return common;
      const url = new URL(request.url);
      if (url.pathname === csvPath) { exports.push(request); return response; }
      if (url.pathname === '/api/v1/findings') lists.push(url);
      return json({ items: count ? [finding()] : [], next_cursor: count ? 'more' : null, total: count } satisfies Schema<'PageFindingView'>);
    }, { task_id: 7, view: 'hidden', decision: 'scored', price_drop: false, over_budget: true, q: '相机 & 镜头', sort: 'latest', cursor: 'screen-page', limit: 17 });

    await screen.findByText(`共 ${count} 条结果`);
    expect(exports).toHaveLength(0);
    await userEvent.setup().click(screen.getByRole('button', { name: '导出 CSV' }));
    await waitFor(() => expect(download).toHaveBeenCalledTimes(1));
    expect(exports).toHaveLength(1);
    expect(exports[0]?.method).toBe('GET');
    const query = new URL(exports[0]?.url ?? 'http://localhost').searchParams;
    expect(Object.fromEntries(query)).toEqual({ view: 'hidden', decision: 'scored', price_drop: 'false', over_budget: 'true', q: '相机 & 镜头', sort: 'latest' });
    expect(lists.map((url) => [url.searchParams.get('cursor'), url.searchParams.get('limit')])).toEqual([['screen-page', '17']]);
    expect(readBlob).toHaveBeenCalledTimes(1);
    expect(download.mock.calls[0]?.[0] === blob).toBe(true);
    expect(download.mock.calls[0]?.slice(1)).toEqual([disposition, 'kakatta-results-7.csv']);
  });

  test('全部任务时说明先选任务，独立收藏和不感兴趣集合无 CSV 入口', async () => {
    let exports = 0;
    await page(async (request) => {
      const common = base(request); if (common) return common;
      if (new URL(request.url).pathname === csvPath) exports++;
      return json({ items: [], next_cursor: null, total: 0 });
    });
    const user = userEvent.setup();
    const button = await screen.findByRole('button', { name: '导出 CSV' });
    expect(button.hasAttribute('disabled')).toBe(true);
    expect(screen.getByText('选择任务后可导出当前筛选下的全部结果。')).toBeTruthy();
    await user.click(button);
    await screen.findByRole('option', { name: '相机' });
    await user.selectOptions(screen.getByRole('combobox', { name: '任务' }), '7');
    expect(screen.getByRole('button', { name: '导出 CSV' }).hasAttribute('disabled')).toBe(false);
    const collections = screen.getByRole('group', { name: '任务结果' });
    for (const name of ['收藏', '不感兴趣']) {
      await user.click(within(collections).getByRole('button', { name }));
      expect(screen.queryByRole('button', { name: '导出 CSV' }) === null).toBe(true);
    }
    expect(exports).toBe(0);
  });

  test.each(['主动取消', '切换筛选再返回', '会话401'] as const)('%s中止 CSV，迟到文件不触发下载', async (mode) => {
    const download = observeDownloads();
    let exports = 0;
    let lost = false;
    let signal: AbortSignal | undefined;
    let late: ((response: Response) => void) | undefined;
    await page(async (request) => {
      const common = base(request); if (common) return common;
      const path = new URL(request.url).pathname;
      if (path === csvPath) {
        exports++;
        signal = request.signal;
        return await new Promise<Response>((resolve) => { late = resolve; });
      }
      if (lost && path === '/api/v1/findings') return json({ error: { code: 'auth.unauthenticated', args: {} }, fields: [], request_id: 'lost-csv-session' } satisfies Schema<'ErrorEnvelope'>, 401);
      return json({ items: [finding()], next_cursor: null, total: 1 } satisfies Schema<'PageFindingView'>);
    }, { task_id: 7, view: 'all', sort: 'latest' });
    const user = userEvent.setup();
    await screen.findByRole('button', { name: '自用相机机身' });
    await user.click(screen.getByRole('button', { name: '导出 CSV' }));
    await waitFor(() => expect(signal !== undefined).toBe(true));
    expect(screen.getByRole('button', { name: '正在准备 CSV…' }).hasAttribute('disabled')).toBe(true);
    if (mode === '主动取消') await user.click(within(screen.getByRole('group', { name: '导出 CSV' })).getByRole('button', { name: '取消' }));
    if (mode === '切换筛选再返回') {
      await user.selectOptions(screen.getByRole('combobox', { name: '排序' }), 'score');
      await user.selectOptions(screen.getByRole('combobox', { name: '排序' }), 'latest');
    }
    if (mode === '会话401') {
      lost = true;
      await user.click(screen.getByRole('button', { name: '刷新' }));
    }
    await waitFor(() => expect(signal?.aborted).toBe(true));
    const response = new Response(csvHeader, { headers: { 'Content-Type': csvMedia } });
    const readBlob = spyOn(response, 'blob');
    await act(async () => { late?.(response); });
    await waitFor(() => expect(readBlob).toHaveBeenCalledTimes(1));
    expect(download).not.toHaveBeenCalled();
    expect(exports).toBe(1);
    expect(screen.queryByRole('button', { name: '正在准备 CSV…' }) === null).toBe(true);
  });

  test.each([
    { name: '503', response: () => json({ error: { code: 'web.network_failed', args: {} }, fields: [], request_id: 'csv-unavailable' } satisfies Schema<'ErrorEnvelope'>, 503), message: '无法连接服务端，请检查网络后重试。' },
    { name: '伪200网页', response: () => new Response('<html>登录</html>', { headers: { 'Content-Type': 'text/html' } }), message: '服务端返回了无法读取的响应。' },
    { name: '空Blob', response: () => new Response('', { headers: { 'Content-Type': csvMedia } }), message: '服务端返回了无法读取的响应。' },
    { name: '长度不符', response: () => new Response(csvHeader, { headers: { 'Content-Type': csvMedia, 'Content-Length': '1' } }), message: '服务端返回了无法读取的响应。' },
    { name: '部分响应206', response: () => new Response(csvHeader, { status: 206, headers: { 'Content-Type': csvMedia } }), message: '服务端返回了无法读取的响应。' },
  ])('$name 显示安全错误，不下载也不自动重试', async ({ name, response, message }) => {
    const download = observeDownloads();
    let exports = 0;
    await page(async (request) => {
      const common = base(request); if (common) return common;
      if (new URL(request.url).pathname === csvPath) { exports++; return response(); }
      return json({ items: [], next_cursor: null, total: 0 } satisfies Schema<'PageFindingView'>);
    }, { task_id: 7 });
    const group = await screen.findByRole('group', { name: '导出 CSV' });
    await userEvent.setup().click(within(group).getByRole('button', { name: '导出 CSV' }));
    const error = await within(group).findByRole('alert');
    expect(error.textContent).toContain(message);
    if (name === '503') expect(error.textContent).toContain('请求编号：csv-unavailable');
    expect(within(group).getByRole('button', { name: '导出 CSV' }).hasAttribute('disabled')).toBe(false);
    expect(download).not.toHaveBeenCalled();
    expect(exports).toBe(1);
  });
});

describe('结果契约映射', () => {
  test('外部地址只接受无凭据的完整HTTP或HTTPS地址', () => {
    for (const value of [null, '', '/item/1', '//example.invalid/item', 'not a url', 'ftp://example.invalid', 'javascript:alert(1)', 'data:image/png,unsafe', 'https://user@example.invalid', 'http://:secret@example.invalid', 'https://user:secret@example.invalid']) expect(safeExternalURL(value)).toBeUndefined();
    expect(safeExternalURL('http://example.invalid/item')).toBe('http://example.invalid/item');
    expect(safeExternalURL('HTTPS://EXAMPLE.INVALID/item')).toBe('https://example.invalid/item');
  });

  test.each(['javascript:alert(1)', 'data:text/html,unsafe', 'https://user:secret@example.invalid/item'])('卡片拒绝危险外部地址 %s，正常地址仍可使用', (url) => {
    const props = { selected: false, selectionFull: false, busy: false, onSelect: () => {}, onOpen: () => {}, onAction: () => {} };
    const view = render(<I18nProvider instance={createI18n()}><FindingCard {...props} finding={finding({ listing: listing({ desktop_url: url, thumbnail_url: url }) })} /></I18nProvider>);
    expect(screen.queryByRole('link', { name: '在闲鱼打开' }) === null).toBe(true);
    expect(screen.getByRole('button', { name: '复制链接' }).hasAttribute('disabled')).toBe(true);
    expect(view.container.querySelector('img') === null).toBe(true);
    expect(screen.getByText('暂无图片')).toBeTruthy();
    view.rerender(<I18nProvider instance={createI18n()}><FindingCard {...props} finding={finding({ listing: listing({ thumbnail_url: 'https://example.invalid/image.jpg' }) })} /></I18nProvider>);
    expect(screen.getByRole('link', { name: '在闲鱼打开' }).getAttribute('href')).toBe('https://example.invalid/item/8001');
    expect(screen.getByRole('button', { name: '复制链接' }).hasAttribute('disabled')).toBe(false);
    expect(view.container.querySelector('img')?.getAttribute('src')).toBe('/api/v1/listings/8001/thumbnail?finding_id=31');
  });

  test('关键词卡片完整显示两个命中词', () => {
    const item = finding({ decision: 'matched', score: null, grade: null, matched_keywords: ['q1', '收音机'] });
    render(<I18nProvider instance={createI18n()}><FindingCard finding={item} selected={false} selectionFull={false} busy={false} onSelect={() => {}} onOpen={() => {}} onAction={() => {}} /></I18nProvider>);
    expect(screen.getAllByText(/^(q1|收音机)$/).map((node) => node.textContent)).toEqual(['q1', '收音机']);
  });

  test.each(['zh-CN', 'en'] as const)('固定风险按ID取%s译名，自定义同名风险保留原文', (locale) => {
    const profile: Schema<'AnalysisProfileFields'> = { summary: '', specs: [], musts: [], bonuses: [], allowed_kinds: [], next_no: 2,
      risks: ['fake', 'u_1'].map((id) => ({ id, name: 'fake', attitude: 'confirm', source: 'user', edited: true, rev: 1 })) };
    const item = finding({ task: { ...finding().task, profile }, checks: ['fake', 'u_1'].map((key) => ({ key, status: 'risk', source: 'program', evidence: null })) });
    render(<I18nProvider instance={createI18n(locale)}><FindingCard finding={item} selected={false} selectionFull={false} busy={false} onSelect={() => {}} onOpen={() => {}} onAction={() => {}} /></I18nProvider>);
    expect(screen.queryByText(locale === 'zh-CN' ? '假货高仿：有风险' : 'Counterfeit or replica: risk found') !== null).toBe(true);
    expect(screen.getByText(locale === 'zh-CN' ? 'fake：有风险' : 'fake: risk found')).toBeTruthy();
  });

  test('详情保留过滤排序而去掉分页和纯界面集合，收藏查询不夹带任务过滤', () => {
    const search: ResultsSearch = { task_id: 7, finding_id: 31, collection: 'favorites', view: 'other', q: '相机', price_drop: true, sort: 'latest', limit: 50, cursor: 'next' };
    expect(findingQuery(search)).toEqual({ task_id: 7, view: 'other', q: '相机', price_drop: true, sort: 'latest', limit: 50, cursor: 'next' });
    expect(detailQuery(search)).toEqual({ task_id: 7, view: 'other', q: '相机', price_drop: true, sort: 'latest' });
    expect(listingQuery(search)).toEqual({ favorite: true, q: '相机', limit: 50, cursor: 'next' });
  });

  test('商品未知价格不当零，选择去重且不超过200，未知时区用明确UTC原文', () => {
    expect(isOverBudget(finding({ listing: listing({ price: null }), task: { ...finding().task, budget_max: 100 } }))).toBe(false);
    expect(selectableIDs(Array.from({ length: 210 }, (_, id) => finding({ id: id + 1 })))).toHaveLength(200);
    expect(selectableIDs([finding(), finding()])).toEqual([31]);
    expect(resultTime(0, 'zh-CN')).toBe('1970-01-01T00:00:00.000Z');
    expect(resultTime(Date.UTC(2026, 0, 1), 'zh-CN', 'Asia/Shanghai')).toContain('08:00:00 (Asia/Shanghai)');
    expect(registrationAge(1461)).toEqual({ years: 4, months: 0 });
    expect(registrationAge(364)).toEqual({ years: 1, months: 0 });
  });

  test('每渠道取最新结果，不让旧成功盖住最近失败', () => {
    const common: Schema<'DeliverySummary'> = { id: 1, channel_id: 9, at: 10, channel_name: 'Bark', task_id: 7, kind: 'recommended', item_count: 1, ok: true, error: null };
    expect(latestDeliveries([common, { ...common, id: 2, at: 20, ok: false }, { ...common, id: 3, channel_id: 10, at: 15 }]).map((item) => item.id)).toEqual([2, 3]);
  });

  test('只展示服务端有效检查项，不用旧事实或手改覆盖重新判断', () => {
    const profile: Schema<'AnalysisProfileFields'> = { summary: '', specs: [], musts: [], bonuses: [], allowed_kinds: [], next_no: 2, risks: [{ id: 'id_lock', attitude: 'confirm', source: 'user', edited: true, rev: 2 }] };
    const item = finding({ task: { ...finding().task, profile }, analysis: { specs: [], brand_new: null, musts: [], bonuses: [], checks: [{ key: 'id_lock', rev: 1, status: 'ok', evidence: '已退出', source: 'text' }], seller_signals: [], price_note: null, extra_findings: [], adjust: { value: 0, reason: '' }, summary: '' }, user_overrides: { risks: { id_lock: { rev: 1, status: 'ok' } } } });
    expect(effectiveRisks(item)[0]?.status).toBe('unknown');
    expect(effectiveRisks({ ...item, checks: [{ key: 'id_lock', status: 'risk', source: 'program', evidence: null }] })[0]?.status).toBe('risk');
  });
});

describe('推送事实展示', () => {
  test('全败可重试、已取消和放弃按真实意图显示，发送前失败不伪造投递', () => {
    const invalid = { code: 'notification.delivery.invalid_message', args: { channel: '本地渠道' } };
    const item = finding({ notify_kind: 'recommended', notification_status: { state: 'retrying', reason: invalid } });
    const content = (current: Finding) => <I18nProvider instance={createI18n()}><NotificationStatus finding={current} /></I18nProvider>;
    const mounted = render(content(item));
    expect(screen.getByText(/推送状态: 下次运行会再发/)).toBeTruthy();
    expect(screen.getByText('本地渠道 发送失败：通知内容超过限制或格式无效')).toBeTruthy();
    expect(screen.queryByText('本地渠道')).toBeNull();
    mounted.rerender(content({ ...item, notification_status: { state: 'cancelled', reason: { code: 'notify.cancelled_dismissed', args: {} } } }));
    expect(screen.getByText(/没有推送：商品已标为不感兴趣，取消待发提醒/)).toBeTruthy();
    expect(screen.queryByText(/下次运行会再发|等待推送/)).toBeNull();
    mounted.rerender(content({ ...item, notification_status: { state: 'abandoned', reason: invalid } }));
    expect(screen.getByText(/没有推送：发送失败：所有渠道都没发出去，不再重发/)).toBeTruthy();
    expect(screen.getByText('本地渠道 发送失败：通知内容超过限制或格式无效')).toBeTruthy();
    expect(screen.queryByText(/下次运行会再发|等待推送/)).toBeNull();
  });

  test('部分渠道成功仍显示失败细节，不承诺自动补发', () => {
    const delivery: Schema<'DeliverySummary'> = { id: 1, at: 1794355200000, channel_id: 1, channel_name: 'Bark', task_id: 7, kind: 'recommended', item_count: 1, ok: true, error: null };
    const item = finding({ notified_at: 1794355200000, deliveries: [delivery, { ...delivery, id: 2, channel_id: 2, channel_name: 'Telegram', ok: false, error: { code: 'notify.error.http_status', args: { status: 401 } } }] });
    render(<I18nProvider instance={createI18n()}><NotificationStatus finding={item} /></I18nProvider>);
    expect(screen.getAllByText('已推送').length).toBeGreaterThan(0);
    expect(screen.getByText('Telegram')).toBeTruthy();
    expect(screen.getByText('推送服务返回 HTTP 401')).toBeTruthy();
    expect(screen.queryByText('下次运行会再发')).toBeNull();
  });

  test('基线未发推荐但后续降价已送达，不显示成从未推送', () => {
    const item = finding({ notify_skip_reason: { code: 'notify.baseline', args: {} }, deliveries: [{ id: 3, at: 1794355200000, channel_id: 1, channel_name: 'Bark', task_id: 7, kind: 'price_drop', item_count: 1, ok: true, error: null }] });
    render(<I18nProvider instance={createI18n()}><NotificationStatus finding={item} /></I18nProvider>);
    expect(screen.getByText('降价')).toBeTruthy();
    expect(screen.getAllByText('已推送').length).toBeGreaterThan(0);
    expect(screen.queryByText(/^没有推送：/)).toBeNull();
  });
});

describe('结果卡近期卖家评价', () => {
  test.each(['zh-CN', 'en'] as const)('%s 只统计最近十条内的卖家差评，并区分中评、无样本和未知', (locale) => {
    const good = { role: 'seller', kind: 'good', text: null };
    const bad = { ...good, kind: 'bad' };
    const neutral = { ...good, kind: 'neutral' };
    const buyerBad = { ...bad, role: 'buyer' };
    const profile: Schema<'SellerProfile'> = {
      seller_id: 'seller', nick: null, avatar_url: null, introduction: null, seller_credit: null, buyer_credit: null,
      seller_credit_level: null, buyer_credit_level: null, item_count: null, rate_count: null, on_sale_count: null,
      sold_count: null, seller_good_rate: 75, buyer_good_rate: null, seller_good_count: 15, seller_rate_count: 20,
      buyer_good_count: null, buyer_rate_count: null, recent_items: null, recent_rates: null, fetched_at: 0,
    };
    const cases: Array<{ reviews: Schema<'SellerProfile'>['recent_rates']; zh: string; en: string }> = [
      { reviews: [bad, buyerBad, neutral, ...Array(7).fill(good), bad], zh: '最近 10 条评价中有 1 条卖家差评', en: 'Negative ratings as a seller in the latest 10 reviews: 1' },
      { reviews: [neutral, buyerBad], zh: '最近 2 条评价中无卖家差评', en: 'No negative ratings as a seller in the latest 2 reviews' },
      { reviews: [], zh: '暂无近期卖家评价', en: 'No recent seller reviews' },
      { reviews: [buyerBad], zh: '暂无近期卖家评价', en: 'No recent seller reviews' },
      { reviews: null, zh: '近期卖家评价未知', en: 'Recent seller reviews unknown' },
      { reviews: [{ ...good, kind: null }], zh: '近期卖家评价未知', en: 'Recent seller reviews unknown' },
      { reviews: [{ ...bad, role: null }], zh: '近期卖家评价未知', en: 'Recent seller reviews unknown' },
    ];
    render(<I18nProvider instance={createI18n(locale)}>{cases.map(({ reviews }, index) => {
      const item = finding({ id: 31 + index, seller: { ...profile, recent_rates: reviews } });
      return <FindingCard key={item.id} finding={item} selected={false} selectionFull={false} busy={false}
        onSelect={() => {}} onOpen={() => {}} onAction={() => {}} />;
    })}</I18nProvider>);
    const articles = screen.getAllByRole('article');
    for (const [index, testCase] of cases.entries()) {
      expect(articles[index]?.textContent).toContain(locale === 'zh-CN' ? testCase.zh : testCase.en);
    }
  });
});

describe('结果历史消息', () => {
  const locales: Locale[] = ['zh-CN', 'en'];
  const marker = (locale: Locale) => locale === 'zh-CN' ? '旧版本消息' : 'Legacy message';
  const knownReason = { code: 'analysis.item_missing', args: { no: 3 } } satisfies Schema<'LocalizedMessage'>;
  const missingReason = { code: knownReason.code, args: {} } satisfies Schema<'LocalizedMessage'>;
  const knownText = (locale: Locale) => locale === 'zh-CN' ? '模型没有返回第 3 件商品，按规格未知处理' : 'The model omitted item 3; its specifications are treated as unknown';

  test.each(locales)('结果卡历史理由 %s 保留原码和标记，完整理由保持正常', (locale) => {
    const reasons = [{ code: 'legacy.finding.reason', args: { ignored: '不能显示的旧参数' } }, missingReason, knownReason];
    const cards = reasons.map((reason, index) => finding({ id: 31 + index, reason, listing: listing({ title: `相机 ${index + 1}` }) }));
    render(<I18nProvider instance={createI18n(locale)}>{cards.map((item) => <FindingCard key={item.id} finding={item} selected={false} selectionFull={false} busy={false}
      onSelect={() => {}} onOpen={() => {}} onAction={() => {}} />)}</I18nProvider>);
    const articles = screen.getAllByRole('article');
    expect(articles[0]?.textContent).toContain('legacy.finding.reason');
    expect(articles[0]?.textContent).toContain(marker(locale));
    expect(articles[0]?.textContent).not.toContain('不能显示的旧参数');
    expect(articles[1]?.textContent).toContain(knownReason.code);
    expect(articles[1]?.textContent).toContain(marker(locale));
    expect(articles[2]?.textContent).toContain(knownText(locale));
    expect(articles[2]?.textContent).not.toContain(marker(locale));
  });

  test.each(locales)('通知历史 %s 保留旧原因和失败原码，正常行保留原句式', (locale) => {
    const delivery: Schema<'DeliverySummary'> = { id: 1, at: 1794355200000, channel_id: 1, channel_name: 'Bark', task_id: 7, kind: 'recommended', item_count: 1, ok: false, error: { code: 'legacy.delivery.failed', args: {} } };
    const deliveries = [delivery,
      { ...delivery, id: 2, channel_id: 2, channel_name: 'Telegram', error: { code: 'notify.error.http_status', args: {} } },
      { ...delivery, id: 3, channel_id: 3, channel_name: 'Webhook', error: { code: 'notify.error.http_status', args: { status: 503 } } },
    ];
    const skip = { code: 'legacy.notify.skipped', args: {} };
    const prefix = locale === 'zh-CN' ? '没有推送：' : 'Not delivered: ';
    const view = render(<I18nProvider instance={createI18n(locale)}><NotificationStatus finding={finding({ notify_skip_reason: skip, deliveries })} /></I18nProvider>);
    expect(screen.queryAllByText(`${prefix}${skip.code}`)).toHaveLength(2);
    expect(view.container.querySelector('summary')?.textContent).toContain(marker(locale));
    for (const [channel, code] of [['Bark', 'legacy.delivery.failed'], ['Telegram', 'notify.error.http_status']] as const) {
      const text = screen.getByText(channel).parentElement?.textContent;
      expect(text).toContain(code);
      expect(text).toContain(marker(locale));
    }
    const normal = screen.getByText('Webhook').parentElement?.textContent;
    expect(normal).toContain(locale === 'zh-CN' ? '推送服务返回 HTTP 503' : 'Notification service returned HTTP 503');
    expect(normal).not.toContain(marker(locale));

    const oldSkip = { code: 'notify.reanalysis_below_recommendation', args: {} };
    view.rerender(<I18nProvider instance={createI18n(locale)}><NotificationStatus finding={finding({ notified_at: 1794355200000, notify_skip_reason: oldSkip, deliveries })} /></I18nProvider>);
    expect(view.container.querySelector('summary')?.textContent).toContain(locale === 'zh-CN' ? '已推送' : 'Delivered');
    expect(screen.queryByText(oldSkip.code)?.parentElement?.textContent).toContain(marker(locale));
    expect(screen.queryByText(`${prefix}${oldSkip.code}`) === null).toBe(true);

    view.rerender(<I18nProvider instance={createI18n(locale)}><NotificationStatus finding={finding({ notify_skip_reason: { code: 'notify.baseline', args: {} }, deliveries: [] })} /></I18nProvider>);
    const baseline = locale === 'zh-CN' ? '首次运行只记录，不发送提醒' : 'Items from the initial run are recorded without sending notifications';
    expect(view.container.querySelector('summary')?.textContent).toContain(`${prefix}${baseline}`);
    expect(view.container.querySelector('summary')?.textContent).not.toContain(marker(locale));
  });

  test.each(locales)('长期无推荐提示 %s 显示历史原码，保留每行数量和正常理由', async (locale) => {
    const unknown = { code: 'legacy.hint.reason', args: {} };
    const hint: Schema<'NoGoodHint'> = { latest_analyzed_at: 1794355200000, sample_count: 7,
      reasons: [{ reason: unknown, count: 4 }, { reason: missingReason, count: 2 }, { reason: knownReason, count: 1 }] };
    await page(async (request) => {
      if (new URL(request.url).pathname === '/api/v1/tasks/7') return json({ ...task(), no_good_hint: hint } satisfies Schema<'TaskView'>);
      return base(request) ?? json({ items: [], next_cursor: null, total: 0 } satisfies Schema<'PageFindingView'>);
    }, { task_id: 7 }, locale);
    const normal = await screen.findByText(`${knownText(locale)}${locale === 'zh-CN' ? '（1 件）' : ' (1 item)'}`);
    expect(normal.textContent).not.toContain(marker(locale));
    for (const [code, count] of [[unknown.code, 4], [missingReason.code, 2]] as const) {
      const suffix = locale === 'zh-CN' ? `（${count} 件）` : ` (${count} items)`;
      expect(screen.queryByText(`${code}${suffix}`)?.parentElement?.textContent).toContain(marker(locale));
    }
  });
});

describe('结果页面实际请求与恢复', () => {
  test('全任务第二页跨任务切换相邻商品后返回原页', async () => {
    const first = finding({ id: 30, item_id: '8000', listing: listing({ item_id: '8000', title: '第一页相机' }) });
    const current = finding({ is_read: true, next_id: 32 });
    const neighbor = finding({ id: 32, task_id: 8, item_id: '8002', listing: listing({ item_id: '8002', title: '另一任务的镜头' }),
      task: { ...current.task, id: 8, name: '镜头' }, is_read: true, previous_id: 31 });
    const detailRequests: Array<{ path: string; query: Record<string, string> }> = [];
    await page(async (request) => {
      const url = new URL(request.url);
      if (url.pathname === '/api/v1/findings') {
        if (url.searchParams.get('cursor') === 'page-two') {
          if (url.searchParams.has('task_id')) return json({ error: { code: 'web.invalid_response', args: {} }, fields: [], request_id: 'wrong-cursor' } satisfies Schema<'ErrorEnvelope'>, 400);
          return json({ items: [current], next_cursor: 'page-three', total: 3 } satisfies Schema<'PageFindingView'>);
        }
        return json({ items: [first], next_cursor: 'page-two', total: 3 } satisfies Schema<'PageFindingView'>);
      }
      if (url.pathname === '/api/v1/findings/31' || url.pathname === '/api/v1/findings/32') {
        detailRequests.push({ path: url.pathname, query: Object.fromEntries(url.searchParams) });
        return json(url.pathname.endsWith('/31') ? current : neighbor);
      }
      if (url.pathname === '/api/v1/risk-dictionary') return json([]);
      if (url.pathname.endsWith('/prices')) return json({ items: [], next_cursor: null, total: 0 });
      return base(request) ?? new Response(null, { status: 404 });
    }, { limit: 1 });

    const user = userEvent.setup();
    await screen.findByRole('button', { name: first.listing.title });
    await user.click(screen.getByRole('button', { name: '下一页' }));
    await user.click(await screen.findByRole('button', { name: current.listing.title }));
    await user.click(await screen.findByRole('button', { name: '下一个商品' }));
    await screen.findByRole('heading', { name: neighbor.listing.title });
    expect((screen.getByRole('button', { name: '上一个商品' }) as HTMLButtonElement).disabled).toBe(false);
    await user.click(screen.getByRole('button', { name: '返回结果' }));

    expect(detailRequests).toEqual([
      { path: '/api/v1/findings/31', query: { view: 'default' } },
      { path: '/api/v1/findings/32', query: { view: 'default' } },
    ]);
    await screen.findByRole('button', { name: current.listing.title });
    expect((screen.getByRole('combobox', { name: '任务' }) as HTMLSelectElement).value).toBe('');
    await waitFor(() => expect((screen.getByRole('button', { name: '上一页' }) as HTMLButtonElement).disabled).toBe(false));
    await user.click(screen.getByRole('button', { name: '上一页' }));
    await screen.findByRole('button', { name: first.listing.title });
  });

  test('单条删除失败保留商品和确认框，204后才删除并刷新', async () => {
    let deleted = false;
    let attempts = 0;
    await page(async (request) => {
      const common = base(request); if (common) return common;
      if (request.method === 'DELETE') {
        attempts++;
        if (attempts === 1) return json({ error: { code: 'web.network_failed', args: {} }, fields: [], request_id: 'delete-request' } satisfies Schema<'ErrorEnvelope'>, 503);
        deleted = true; return new Response(null, { status: 204 });
      }
      return json({ items: deleted ? [] : [finding()], next_cursor: null, total: deleted ? 0 : 1 } satisfies Schema<'PageFindingView'>);
    });
    const user = userEvent.setup();
    await screen.findByRole('button', { name: '自用相机机身' });
    await user.click(screen.getByRole('button', { name: '更多操作' }));
    await user.click(screen.getByRole('menuitem', { name: '删除结果' }));
    const dialog = screen.getByRole('alertdialog');
    await user.click(within(dialog).getByRole('button', { name: '确认' }));
    await waitFor(() => expect(within(dialog).getByRole('alert').textContent).toContain('无法连接服务端'));
    expect(screen.getByText('自用相机机身')).toBeTruthy();
    await user.click(within(dialog).getByRole('button', { name: '确认' }));
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());
    await waitFor(() => expect(screen.queryByRole('button', { name: '自用相机机身' })).toBeNull());
    expect(attempts).toBe(2);
  });

  test('重分析先按所选编号预检，202只报接纳，不自动立即运行', async () => {
    const calls: Array<{ method: string; path: string; body: unknown }> = [];
    await page(async (request) => {
      const path = new URL(request.url).pathname;
      calls.push({ method: request.method, path, body: request.method === 'POST' ? await request.clone().json().catch(() => null) : null });
      const common = base(request); if (common) return common;
      if (path.endsWith('reanalysis-preview')) return json({ as_of: 1794355200000, requested_count: 1, needs_detail_count: 1, cached_detail_count: 0 } satisfies Schema<'ReanalysisPreview'>);
      if (path.endsWith('/reanalyze')) return json({ items: [{ finding_id: 31, status: 'queued', reason: null, needs_detail: true }], needs_detail_count: 1 } satisfies Schema<'ReanalysisResult'>, 202);
      return json({ items: [finding()], next_cursor: null, total: 1 } satisfies Schema<'PageFindingView'>);
    });
    const user = userEvent.setup();
    await screen.findByRole('button', { name: '自用相机机身' });
    await user.click(screen.getByRole('button', { name: '更多操作' }));
    await user.click(screen.getByRole('menuitem', { name: '重新分析' }));
    const dialog = screen.getByRole('alertdialog');
    await waitFor(() => expect(within(dialog).getByRole('button', { name: '确认' }).hasAttribute('disabled')).toBe(false));
    expect(dialog.textContent).toContain('其中 1 个需要重新打开详情');
    expect(dialog.textContent).toContain('不会预扣额度');
    await user.click(within(dialog).getByRole('button', { name: '确认' }));
    await screen.findByText('已接纳 1 条重新分析请求，请查看后续状态');
    expect(calls.find((call) => call.path.endsWith('reanalysis-preview'))?.body).toEqual({ ids: [31] });
    expect(calls.some((call) => call.path.endsWith('/31/reanalyze'))).toBe(true);
    expect(calls.some((call) => call.path.endsWith('/run'))).toBe(false);
  });

  test('切换筛选会取消旧读取，旧页不能覆盖新集合', async () => {
    let oldAborted = false;
    await page(async (request) => {
      const common = base(request); if (common) return common;
      const url = new URL(request.url);
      if (url.searchParams.get('view') === 'all') return json({ items: [finding({ listing: listing({ title: '全部集合的商品' }) })], next_cursor: null, total: 1 } satisfies Schema<'PageFindingView'>);
      return await new Promise<Response>((_resolve, reject) => request.signal.addEventListener('abort', () => { oldAborted = true; reject(new DOMException('取消', 'AbortError')); }, { once: true }));
    });
    const user = userEvent.setup();
    await user.selectOptions(await screen.findByRole('combobox', { name: '显示范围' }), 'all');
    await screen.findByRole('button', { name: '全部集合的商品' });
    expect(oldAborted).toBe(true);
  });

  test.each([
    { name: '混合重分析', statuses: ['queued', 'cached', 'rejected'], queued: 1, status: 202 },
    { name: '全部拒绝HTTP200', statuses: ['rejected', 'rejected', 'rejected'], queued: 0, status: 200 },
    { name: '缓存与拒绝HTTP200', statuses: ['cached', 'cached', 'rejected'], queued: 0, status: 200 },
  ] satisfies Array<{ name: string; statuses: Schema<'ReanalysisStatus'>[]; queued: number; status: number }>)('$name 只统计实际排队项，保留全部逐项状态和原因', async ({ statuses, queued, status }) => {
    const items = statuses.map((_value, index) => finding({ id: index + 31, item_id: String(8001 + index) }));
    const results = statuses.map((value, index): Schema<'ReanalysisItem'> => ({ finding_id: index + 31, status: value, needs_detail: value === 'queued', reason: value === 'rejected' ? { code: 'findings.not_found', args: {} } : null }));
    await page(async (request) => {
      const common = base(request); if (common) return common;
      const path = new URL(request.url).pathname;
      if (path.endsWith('reanalysis-preview')) return json({ as_of: 1794355200000, requested_count: 3, needs_detail_count: 1, cached_detail_count: 2 } satisfies Schema<'ReanalysisPreview'>);
      if (request.method === 'POST') return json({ items: results, needs_detail_count: queued } satisfies Schema<'ReanalysisResult'>, status);
      return json({ items, next_cursor: null, total: items.length } satisfies Schema<'PageFindingView'>);
    });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '选择当前筛选，最多 200 条' }));
    await user.click(await screen.findByRole('button', { name: '重新分析所选结果' }));
    const dialog = screen.getByRole('alertdialog');
    await waitFor(() => expect(within(dialog).getByRole('button', { name: '确认' }).hasAttribute('disabled')).toBe(false));
    await user.click(within(dialog).getByRole('button', { name: '确认' }));
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());
    for (const result of results) {
      const text = result.status === 'queued' ? '已排队，等待正常运行处理' : result.status === 'cached' ? '已使用缓存详情重新判断' : '未通过当前条件';
      const row = screen.getByText((_, element) => element?.tagName === 'LI' && element.textContent?.startsWith(`#${result.finding_id} · ${text}`) === true);
      if (result.status === 'rejected') expect(row.textContent).toContain('这条结果已不存在');
    }
    if (queued > 0) expect(screen.queryByText(`已接纳 ${queued} 条重新分析请求，请查看后续状态`) !== null).toBe(true);
    else expect(screen.queryByText(/^已接纳 \d+ 条重新分析请求/) === null).toBe(true);
    expect(screen.queryByText('已接纳 3 条重新分析请求，请查看后续状态') === null).toBe(true);
  });

  test('全部失败215条按筛选顺序只预检并提交前200条，剩余另行确认', async () => {
    const items = failedItems();
    let remaining = items;
    const collected: URL[] = [];
    const previews: number[][] = [];
    const batches: number[][] = [];
    await page(async (request) => {
      const common = base(request); if (common) return common;
      const url = new URL(request.url);
      if (request.method === 'POST') {
        const body: Schema<'FindingBatchRequest'> = await request.json();
        if (url.pathname.endsWith('reanalysis-preview')) { previews.push(body.ids); return batchPreview(body.ids); }
        batches.push(body.ids);
        remaining = remaining.filter((item) => !body.ids.includes(item.id));
        return batchReanalysis(body.ids);
      }
      if (url.searchParams.get('limit') === '200') collected.push(url);
      return failedPage(request, remaining);
    }, { ...allFailedSearch, view: 'hidden' });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '重新分析全部失败的' }));
    const dialog = await screen.findByRole('alertdialog');
    expect(previews.map((ids) => ids.length)).toEqual([200]);
    expect(dialog.textContent).toContain('215 条失败结果');
    expect(dialog.textContent).toContain('这次处理前 200 条，剩下的再点一次');
    expect(dialog.textContent).toContain('200 条可使用缓存详情');
    expect(dialog.textContent).toContain('0 条需要重新打开详情');
    expect(dialog.textContent).toContain('不会预扣额度');
    expect(batches).toHaveLength(0);
    expect(collected.map((url) => url.searchParams.get('cursor'))).toEqual([null]);
    for (const url of collected) for (const [key, value] of Object.entries({ task_id: '7', view: 'hidden', decision: 'failed', q: '机身', price_drop: 'true', over_budget: 'true', sort: 'latest' })) expect(url.searchParams.get(key)).toBe(value);
    await user.click(within(dialog).getByRole('button', { name: '确认' }));
    await screen.findByText('已接纳 200 条重新分析请求，请查看后续状态');
    expect(batches).toEqual(previews);
    expect(batches.flat()).toEqual(items.slice(0, 200).map((item) => item.id));
    expect(screen.queryByText((_, element) => element?.tagName === 'LI' && element.textContent?.startsWith('#245 · 已排队') === true)).toBeNull();

    await waitFor(() => expect(screen.getByRole('button', { name: '重新分析全部失败的' }).hasAttribute('disabled')).toBe(false));
    await user.click(screen.getByRole('button', { name: '重新分析全部失败的' }));
    const rest = await screen.findByRole('alertdialog');
    expect(rest.textContent).toContain('15 条失败结果');
    expect(rest.textContent).toContain('15 条需要重新打开详情');
    expect(rest.textContent).not.toContain('剩下的再点一次');
    expect(batches).toHaveLength(1);
    await user.click(within(rest).getByRole('button', { name: '确认' }));
    await screen.findByText('已接纳 15 条重新分析请求，请查看后续状态');
    expect(batches).toEqual(previews);
    expect(batches.flat()).toEqual(items.map((item) => item.id));
    expect(screen.getByText((_, element) => element?.tagName === 'LI' && element.textContent?.startsWith('#245 · 已排队') === true)).toBeTruthy();
  });

  test('全部失败预检401立即中止，之后不确认也不入队', async () => {
    const items = failedItems();
    let previews = 0;
    let submitted = 0;
    let failedSignal: AbortSignal | undefined;
    await page(async (request) => {
      const common = base(request); if (common) return common;
      if (request.method === 'POST') {
        if (new URL(request.url).pathname.endsWith('reanalysis-preview')) {
          previews++;
          failedSignal = request.signal;
          return json({ error: { code: 'auth.unauthenticated', args: {} }, fields: [], request_id: 'lost-session' } satisfies Schema<'ErrorEnvelope'>, 401);
        }
        submitted++; return json({});
      }
      return failedPage(request, items);
    }, allFailedSearch);
    await userEvent.setup().click(await screen.findByRole('button', { name: '重新分析全部失败的' }));
    await waitFor(() => expect(failedSignal?.aborted).toBe(true));
    expect(previews).toBe(1);
    expect(submitted).toBe(0);
    expect(screen.queryByRole('alertdialog') === null).toBe(true);
  });

  test('全部失败收集时切筛选会取消，忽略不服从取消的旧响应', async () => {
    const items = failedItems();
    let late: ((response: Response) => void) | undefined;
    let signal: AbortSignal | undefined;
    let posts = 0;
    await page(async (request) => {
      const common = base(request); if (common) return common;
      const url = new URL(request.url);
      if (request.method === 'POST') { posts++; return json({}); }
      if (url.searchParams.get('limit') === '200') {
        signal = request.signal;
        return await new Promise<Response>((resolve) => { late = resolve; });
      }
      return failedPage(request, items);
    }, allFailedSearch);
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '重新分析全部失败的' }));
    await waitFor(() => expect(signal !== undefined).toBe(true));
    await user.selectOptions(screen.getByRole('combobox', { name: '显示范围' }), 'default');
    expect(signal?.aborted).toBe(true);
    await act(async () => { late?.(json({ items: items.slice(0, 200), next_cursor: 'failed-next', total: 215 } satisfies Schema<'PageFindingView'>)); });
    expect(posts).toBe(0);
    expect(screen.queryByRole('alertdialog') === null).toBe(true);
  });

  test('全部失败可取消预检，晚响应不会弹确认或入队', async () => {
    const items = failedItems();
    let signal: AbortSignal | undefined;
    let late: ((response: Response) => void) | undefined;
    let submitted = 0;
    await page(async (request) => {
      const common = base(request); if (common) return common;
      if (request.method === 'POST') {
        if (new URL(request.url).pathname.endsWith('reanalysis-preview')) {
          signal = request.signal;
          return await new Promise<Response>((resolve) => { late = resolve; });
        }
        submitted++; return json({});
      }
      return failedPage(request, items);
    }, allFailedSearch);
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '重新分析全部失败的' }));
    await waitFor(() => expect(signal !== undefined).toBe(true));
    await user.click(screen.getByRole('button', { name: '取消' }));
    expect(signal?.aborted).toBe(true);
    await act(async () => { late?.(batchPreview(items.slice(0, 200).map((item) => item.id))); });
    expect(submitted).toBe(0);
    expect(screen.queryByRole('alertdialog') === null).toBe(true);
  });

  test('全部失败入队请求失败不自动重试，也不把未确认结果当成功', async () => {
    const items = failedItems();
    const batches: number[][] = [];
    await page(async (request) => {
      const common = base(request); if (common) return common;
      if (request.method === 'POST') {
        const body: Schema<'FindingBatchRequest'> = await request.json();
        if (new URL(request.url).pathname.endsWith('reanalysis-preview')) return batchPreview(body.ids);
        batches.push(body.ids);
        return json({ error: { code: 'web.network_failed', args: {} }, fields: [], request_id: 'failed-batch' } satisfies Schema<'ErrorEnvelope'>, 503);
      }
      return failedPage(request, items);
    }, allFailedSearch);
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '重新分析全部失败的' }));
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: '确认' }));
    await screen.findByText('操作已停止：已确认 0 条结果，其中 0 条已排队；200 条请求结果未确认，0 条尚未提交。未自动重发。');
    expect(batches.map((ids) => ids.length)).toEqual([200]);
    expect(screen.queryByRole('alertdialog') === null).toBe(true);
    expect(screen.queryByText(/^已接纳 \d+ 条重新分析请求/)).toBeNull();
  });

  test.each(['取消', '切筛选后返回'] as const)('全部失败入队时%s，迟到的响应不计成功', async (mode) => {
    const items = failedItems();
    let batches = 0;
    let signal: AbortSignal | undefined;
    let late: ((response: Response) => void) | undefined;
    await page(async (request) => {
      const common = base(request); if (common) return common;
      if (request.method === 'POST') {
        const body: Schema<'FindingBatchRequest'> = await request.json();
        if (new URL(request.url).pathname.endsWith('reanalysis-preview')) return batchPreview(body.ids);
        batches++;
        signal = request.signal;
        return await new Promise<Response>((resolve) => { late = resolve; });
      }
      return failedPage(request, items);
    }, allFailedSearch);
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '重新分析全部失败的' }));
    const dialog = await screen.findByRole('alertdialog');
    await user.click(within(dialog).getByRole('button', { name: '确认' }));
    await waitFor(() => expect(signal !== undefined).toBe(true));
    if (mode === '取消') await user.click(within(dialog).getByRole('button', { name: '取消' }));
    else {
      fireEvent.change(screen.getByLabelText('显示范围', { selector: 'select' }), { target: { value: 'default' } });
      await waitFor(() => expect(screen.queryByRole('alertdialog') === null).toBe(true));
      await user.selectOptions(screen.getByRole('combobox', { name: '显示范围' }), 'all');
    }
    expect(signal?.aborted).toBe(true);
    if (mode === '取消') await screen.findByText('操作已停止：已确认 0 条结果，其中 0 条已排队；200 条请求结果未确认，0 条尚未提交。未自动重发。');
    await act(async () => { late?.(batchReanalysis(items.slice(0, 200).map((item) => item.id))); });
    expect(batches).toBe(1);
    expect(screen.queryByText(/^已接纳 \d+ 条重新分析请求/)).toBeNull();
    expect(screen.queryByText((_, element) => element?.tagName === 'LI' && element.textContent?.startsWith('#31 · 已排队') === true)).toBeNull();
  });

  test('批量跳过项显示原因并保留选择，不把部分应用说成全成功', async () => {
    let changed = false;
    let batchBody: unknown;
    await page(async (request) => {
      const common = base(request); if (common) return common;
      if (request.method === 'POST') {
        batchBody = await request.json();
        changed = true;
        return json({ requested: 2, applied: 1, skipped: 1, items: [{ id: 31, status: 'applied', reason: null }, { id: 32, status: 'skipped', reason: { code: 'findings.not_found', args: {} } }] } satisfies Schema<'BatchResult'>);
      }
      return json({ items: changed ? [] : [finding(), finding({ id: 32, item_id: '8002', listing: listing({ item_id: '8002', title: '另一台相机' }) })], next_cursor: null, total: changed ? 0 : 2 } satisfies Schema<'PageFindingView'>);
    });
    const user = userEvent.setup();
    await screen.findByRole('button', { name: '自用相机机身' });
    await user.click(screen.getByRole('button', { name: '选择当前筛选，最多 200 条' }));
    await user.click(await screen.findByRole('button', { name: '删除所选结果' }));
    await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: '确认' }));
    await screen.findByText('有 1 条结果未处理');
    expect(batchBody).toEqual({ ids: [31, 32], action: 'delete' });
    expect(screen.getByRole('region', { name: '已选 1 项' })).toBeTruthy();
    expect(screen.getByText(/#32/).textContent).toContain('32');
  });

  test('已隐藏的孤立商品可撤销，不依赖任务发现或触发重新分析', async () => {
    let restored = false;
    let patch: unknown;
    const calls: string[] = [];
    const saved = { ...listing({ is_dismissed: true }), findings: [], prices: [], seller: null } satisfies Schema<'ListingView'>;
    await page(async (request) => {
      const path = new URL(request.url).pathname;
      calls.push(path);
      const common = base(request); if (common) return common;
      if (request.method === 'PATCH') { patch = await request.json(); restored = true; return json({ ...saved, is_dismissed: false }); }
      return json({ items: restored ? [] : [saved], next_cursor: null, total: restored ? 0 : 1 } satisfies Schema<'PageListingView'>);
    }, { collection: 'dismissed' });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '撤销不感兴趣' }));
    await waitFor(() => expect(screen.queryByRole('button', { name: '自用相机机身' })).toBeNull());
    expect(patch).toEqual({ is_dismissed: false });
    expect(calls.some((path) => path.includes('/findings') || path.includes('/reanalyze'))).toBe(false);
  });

  test('独立收藏价格按游标继续读取，刷新失败仍保留已读商品', async () => {
    let failItemRefresh = false;
    const priceCursors: Array<string | null> = [];
    const saved = { ...listing({ is_favorite: true, desktop_url: 'https://user:secret@example.invalid/item', thumbnail_url: 'data:image/svg+xml,unsafe' }), findings: [], prices: [], seller: null } satisfies Schema<'ListingView'>;
    const api = createApiClient({ fetch: async (request) => {
      const url = new URL(request.url);
      const common = base(request); if (common) return common;
      if (url.pathname.endsWith('/prices')) {
        const cursor = url.searchParams.get('cursor');
        priceCursors.push(cursor);
        return json({ items: [{ id: cursor ? 2 : 1, item_id: '8001', price: cursor ? 180000 : 190000, observed_at: 1794355200000 }], next_cursor: cursor ? null : 'older', total: 2 } satisfies Schema<'PagePricePoint'>);
      }
      if (failItemRefresh) return json({ error: { code: 'web.network_failed', args: {} }, fields: [], request_id: 'refresh-request' } satisfies Schema<'ErrorEnvelope'>, 503);
      return json(saved);
    } });
    const client = createAppQueryClient();
    clients.push(client);
    render(<QueryClientProvider client={client}><I18nProvider instance={createI18n()}><ApiProvider api={api}><ListingPanel itemId="8001" /></ApiProvider></I18nProvider></QueryClientProvider>);
    await screen.findByRole('heading', { name: '自用相机机身' });
    expect(screen.queryByRole('link', { name: '在闲鱼打开' }) === null).toBe(true);
    expect(screen.getByRole('button', { name: '复制链接' }).hasAttribute('disabled')).toBe(true);
    expect(screen.getByText('暂无图片')).toBeTruthy();
    await userEvent.setup().click(await screen.findByRole('button', { name: '读取更多价格记录' }));
    await waitFor(() => expect(priceCursors).toEqual([null, 'older']));
    failItemRefresh = true;
    await act(async () => { await client.invalidateQueries({ queryKey: apiKey('listings', '8001'), exact: true }); });
    await screen.findByText('更新失败，仍显示上次读取的结果', { exact: false });
    expect(screen.getByRole('heading', { name: '自用相机机身' })).toBeTruthy();
  });

  test('输入法确认不提前提交搜索，普通提交保留完整文本', async () => {
    const changes: Array<Partial<ResultsSearch>> = [];
    render(<I18nProvider instance={createI18n()}><ResultsFilters search={{}} tasks={[]} tasksHaveMore={false} tasksLoading={false} onMoreTasks={() => {}} onChange={(patch) => changes.push(patch)} /></I18nProvider>);
    const input = screen.getByRole('searchbox', { name: '搜索已有结果' });
    fireEvent.compositionStart(input);
    fireEvent.change(input, { target: { value: '相机 256G' } });
    fireEvent.keyDown(input, { key: 'Enter', isComposing: true });
    expect(changes).toHaveLength(0);
    fireEvent.compositionEnd(input);
    await userEvent.setup().click(screen.getByRole('button', { name: '搜索' }));
    expect(changes).toEqual([{ q: '相机 256G' }]);
  });
});
