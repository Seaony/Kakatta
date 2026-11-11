import { expect, test } from 'bun:test';
import type { ReactNode } from 'react';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClientProvider } from '@tanstack/react-query';
import type { components } from '../../api/schema.gen';
import { createApiClient, type ApiFetch } from '../../api/client';
import { ApiError } from '../../api/errors';
import { ApiProvider } from '../../api/hooks';
import { createAppQueryClient } from '../../app/query-client';
import { createI18n, I18nProvider } from '../../i18n';
import { DailyPrices } from './daily-prices';
import { formatMarketPercent, formatStatisticMoney, safeListingURL } from './format';
import { MarketContent, MarketInsightSection, MarketPanel } from './market-panel';

type Group = components['schemas']['AnalysisMarketGroup'];
type Market = components['schemas']['MarketView'];
type Sample = components['schemas']['MarketSampleView'];

function group(): Group {
  return {
    config_key: 'mini 6 / 256G / WiFi', specs: [{ name: '型号', value: 'mini 6' }],
    brand_new: false, selected: true,
    fair: { price: 190000, sample: 6, basis: { code: 'analysis.fair_relaxed', args: { days: 30, count: 30, name: '网络' } }, days: 30, dropped_spec: '网络', item_ids: ['1', '2', '3', '4', '5', '6'] },
    samples: [], price_band: { p25: 190000.25, p75: 200000.75 },
    daily: [
      { day: '2026-10-28', median: 100005, sample: 3 },
      { day: '2026-10-29', median: null, sample: 0 },
      { day: '2026-11-10', median: 110005.5, sample: 4 },
    ],
    trend: { first_day: '2026-10-28', last_day: '2026-11-10', first_median: 100005, last_median: 110005.5, change: 10000.5, percent: 0.1 },
    cheapest: { item_id: 'cheap', seller_id: '', price: 150000, day: '2026-11-10', last_seen_at: 1794297600000 },
  };
}

function data(groups: Group[] = [group()]): Market {
  return { market: { groups, snapshot_at: 1794297600000, task_trend: [], warnings: [] }, insight: null, today_new_count: 0, today_recommended_count: 0, personal_seller_share: null };
}

function taskView(): components['schemas']['TaskView'] {
  return {
    id: 7, name: '本地行情任务', keyword: '相机', enabled: true, account_id: null, account_pinned: false,
    min_price: null, max_price: null, personal_only: true, free_shipping_only: false,
    publish_window: 'any', region_province: null, region_city: null, region_district: null,
    max_pages: 1, interval_minutes: 30, crawl_overrides: {}, judge_mode: 'keyword', requirement: '', profile: {},
    budget_min: null, budget_max: null, match_keywords: [], exclude_keywords: [], analyze_on_first_run: true,
    notify_policy: 'recommended', notify_price_drop: true, channel_ids: [], search_generation: 1,
    baseline_pending: false, sort_index: 0, created_at: null, updated_at: null, last_run_at: null,
    last_success_at: null, next_run_at: null, last_run_outcome: null, last_error: null,
    consecutive_failures: 0, failure_paused_until: null, no_good_hint_dismissed_at: null,
    runtime: { task_id: 7, phase: 'idle', run_id: null, account_id: null, queued_at: null, started_at: null, blocked: null },
    finding_counts: {}, no_good_hint: null,
  };
}

function provider(): components['schemas']['ProviderView'] {
  return { id: 1, name: '本地测试服务商', preset: null, base_url: 'http://models.invalid', vision_model: 'local-vision', text_model: null, api_key_set: false, enabled: false, sort_index: 0, error_state: { code: 'web.network_failed', args: {} }, error_at: 1794297600000 };
}

function marketFetch(providers: () => Response, currentMarket: Market = data(), requests: Request[] = []): ApiFetch {
  return async (request) => {
    requests.push(request);
    switch (new URL(request.url).pathname) {
      case '/api/v1/tasks/7': return Response.json(taskView());
      case '/api/v1/settings': return Response.json({ values: { 'general.timezone': 'UTC' }, sources: {}, warnings: [] });
      case '/api/v1/tasks/7/market': return Response.json(currentMarket);
      case '/api/v1/ai/providers': return providers();
      default: throw new Error('测试不允许未声明的请求');
    }
  };
}

function wrapper(children: ReactNode, fetch: ApiFetch = async () => Response.json({})) {
  const queryClient = createAppQueryClient();
  const api = createApiClient({ fetch });
  return { queryClient, ...render(<I18nProvider instance={createI18n('zh-CN')}><QueryClientProvider client={queryClient}><ApiProvider api={api}>{children}</ApiProvider></QueryClientProvider></I18nProvider>) };
}

test('统计金额保留四分之一分和半分，趋势比率0.1只显示10%，不截为整数分', () => {
  expect(formatStatisticMoney(190000.25, 'en')).toBe('CN¥1,900.0025');
  expect(formatStatisticMoney(100.5, 'zh-CN')).toBe('¥1.005');
  expect(formatMarketPercent(0.1, 'zh-CN')).toBe('+10%');
  expect(formatMarketPercent(-0.1, 'en')).toBe('-10%');
  expect(formatMarketPercent(0.00000123, 'en')).toBe('+0.000123%');
  expect(safeListingURL('javascript:alert(1)')).toBeUndefined();
});

test('配置总览只显示勾选组，放宽依据、浮点价格带、14日日期和最低价分别展示', () => {
  const hidden = { ...group(), config_key: '不应出现的未勾选配置', selected: false };
  wrapper(<MarketContent taskId={7} data={data([group(), hidden])} mode="ai" timezone="Asia/Singapore" onRefresh={() => {}} />);
  expect(screen.queryByText('不应出现的未勾选配置')).toBeNull();
  expect(screen.getByText('¥1,900.0025 – ¥2,000.0075')).toBeDefined();
  expect(screen.getByText('放宽到 30 天 · 不分网络')).toBeDefined();
  expect(screen.getByText('+10%')).toBeDefined();
  expect(screen.getByText('比较 2026-10-28 与 2026-11-10')).toBeDefined();
  expect(screen.getByRole('button', { name: '¥1,500.00' })).toBeDefined();
  expect(screen.queryByRole('heading', { name: 'AI 行情解读' })).toBeNull();
});

test('无法比价保留样本、已放宽范围和每日数据，不把缺日或缺涨跌画成零', async () => {
  const unavailable = group();
  unavailable.fair = { ...unavailable.fair, price: null, sample: 4, basis: { code: 'analysis.fair_unavailable', args: { count: 4 } } };
  unavailable.trend = null;
  unavailable.cheapest = null;
  wrapper(<MarketContent taskId={7} data={data([unavailable])} mode="ai" timezone="UTC" onRefresh={() => {}} />);
  expect(screen.getByText('无法比价')).toBeDefined();
  expect(screen.getByText('4 个样本')).toBeDefined();
  expect(screen.getByText('近 30 天')).toBeDefined();
  expect(screen.getByText('不分网络')).toBeDefined();
  expect(screen.getByText('最近一次运行未搜到可比商品')).toBeDefined();
  const summary = screen.getByText('查看每日数据');
  fireEvent.click(summary);
  const table = screen.getByRole('table');
  expect(within(table).getByText('当天无样本')).toBeDefined();
  expect(within(table).getByText('¥1,100.055')).toBeDefined();
});

test('行情汇总显示当天发现和推荐数，个人卖家未知不显示为零', () => {
  for (const [share, label] of [[null, '—'], [0, '0%'], [2 / 3, '66.7%']] as const) {
    const market = { ...data([]), today_new_count: 12, today_recommended_count: 7, personal_seller_share: share };
    const { unmount } = wrapper(<MarketContent taskId={7} data={market} mode="ai" timezone="UTC" onRefresh={() => {}} />);
    const stat = (name: string) => screen.getByText(name, { selector: 'dt' }).nextElementSibling?.textContent;
    expect(stat('今日新增')).toBe('12');
    expect(stat('今日推荐')).toBe('7');
    expect(stat('近 14 天个人卖家占比')).toBe(label);
    if (share === null) expect(screen.getByTitle('暂无样本').textContent).toBe('—');
    unmount();
  }
});

test('日图缺口处分段，数据表提供同一组完整数值', () => {
  const points = group().daily;
  wrapper(<DailyPrices config="mini 6" points={points} />);
  const chart = screen.getByRole('img', { name: 'mini 6每日价格与样本数' });
  expect(chart.querySelectorAll('path')).toHaveLength(2);
  expect(chart.querySelectorAll('circle')).toHaveLength(2);
  expect(screen.getByText('查看每日数据')).toBeDefined();
});

test('关键词任务只显示中位数、价格带和每日走势，不冒用全新/二手或合理价列', () => {
  const keyword = { ...group(), config_key: '*', specs: [] };
  wrapper(<MarketContent taskId={8} data={data([keyword])} mode="keyword" timezone="UTC" onRefresh={() => {}} />);
  expect(screen.getByRole('article', { name: '不分配置' })).toBeDefined();
  expect(screen.queryByText('合理价')).toBeNull();
  expect(screen.queryByText('现在最便宜')).toBeNull();
  expect(screen.queryByText('较 14 天前')).toBeNull();
});

test('样本按真实配置与新旧请求，翻页保留前页，并可从下一页失败恢复', async () => {
  const requests: URL[] = [];
  let failNext = true;
  const sample: Sample = { item_id: '1', title: '本地测试商品一', price: 190000, seller_id: null, observed_at: 1794297600000, config_key: 'mini 6 / 256G / 蜂窝', brand_new: false, desktop_url: 'https://example.invalid/item/1' };
  const failure = { error: { code: 'web.network_failed', args: {} }, fields: [], request_id: 'sample-page' };
  wrapper(<MarketContent taskId={7} data={data()} mode="ai" timezone="UTC" onRefresh={() => {}} />, async (request) => {
    const url = new URL(request.url); requests.push(url);
    if (url.searchParams.get('cursor') === 'second' && failNext) { failNext = false; return Response.json(failure, { status: 503 }); }
    return url.searchParams.has('cursor') ? Response.json({ items: [{ ...sample, item_id: '2', title: '本地测试商品二' }], total: 2, next_cursor: null }) : Response.json({ items: [sample], total: 2, next_cursor: 'second' });
  });
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: '查看mini 6 / 256G / WiFi · 二手的比价样本' }));
  const dialog = await screen.findByRole('dialog', { name: 'mini 6 / 256G / WiFi · 二手的比价样本' });
  await within(dialog).findByText('本地测试商品一');
  expect(requests[0]?.pathname).toBe('/api/v1/tasks/7/market/samples');
  expect(requests[0]?.searchParams.get('config_key')).toBe('mini 6 / 256G / WiFi');
  expect(requests[0]?.searchParams.get('brand_new')).toBe('false');
  expect(requests[0]?.searchParams.get('limit')).toBe('50');
  expect(within(dialog).getByText('mini 6 / 256G / 蜂窝 · 二手')).toBeDefined();
  await user.click(within(dialog).getByRole('button', { name: '加载更多样本' }));
  await within(dialog).findByRole('alert');
  expect(within(dialog).getByText('本地测试商品一')).toBeDefined();
  await user.click(within(dialog).getByRole('button', { name: '重试' }));
  await within(dialog).findByText('本地测试商品二');
  expect(within(dialog).getByText('已显示 2 / 2 个样本')).toBeDefined();
  expect(within(dialog).queryByRole('button', { name: '加载更多样本' })).toBeNull();
  await user.click(within(dialog).getByRole('button', { name: '关闭' }));
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
});

test('刷新失败保留原数据和选择，未知原因安全回退', async () => {
  let calls = 0;
  wrapper(<MarketContent taskId={7} data={data()} mode="ai" timezone="UTC" error={new ApiError(503, { code: 'unregistered.private_message', args: { private: 'not-visible' } })} onRefresh={() => { calls++; }} />);
  expect(screen.getByText('刷新失败，当前显示上次数据。')).toBeDefined();
  expect(screen.getByText('请求未完成，请重试。')).toBeDefined();
  expect(screen.queryByText('not-visible')).toBeNull();
  expect(screen.getByText('¥1,900.00')).toBeDefined();
  await userEvent.setup().click(screen.getByRole('button', { name: '重试' }));
  expect(calls).toBe(1);
});

test('解读按配置事实隐藏或显示空态，已有模型原文作为文本保留', () => {
  const insight: components['schemas']['MarketInsight'] = { id: 1, task_id: 7, generated_at: 1794297600000, stats: null, text: '<b>本地测试原文</b>', provider_id: 2, model: 'sample-model' };
  const { rerender } = wrapper(<MarketInsightSection configured insight={insight} timezone="UTC" />);
  expect(screen.getByText('<b>本地测试原文</b>').querySelector('b')).toBeNull();
  rerender(<I18nProvider instance={createI18n()}><MarketInsightSection configured={false} insight={insight} timezone="UTC" /></I18nProvider>);
  expect(screen.queryByRole('heading', { name: 'AI 行情解读' })).toBeNull();
  rerender(<I18nProvider instance={createI18n()}><MarketInsightSection configured insight={null} timezone="UTC" /></I18nProvider>);
  expect(screen.getByText('还没有行情解读')).toBeDefined();
  expect(screen.queryByRole('button')).toBeNull();
});

test('行情面板卸载取消所有在途只读请求，不启动模型或写操作', async () => {
  const requests: Request[] = [];
  const { unmount } = wrapper(<MarketPanel taskId={7} />, async (request) => {
    requests.push(request);
    return new Promise<Response>((_resolve, reject) => {
      request.signal.addEventListener('abort', () => reject(request.signal.reason), { once: true });
    });
  });
  await waitFor(() => expect(requests).toHaveLength(4));
  expect(requests.every((request) => request.method === 'GET')).toBe(true);
  expect(requests.some((request) => new URL(request.url).pathname === '/api/v1/tasks/7/market')).toBe(true);
  unmount();
  await waitFor(() => expect(requests.every((request) => request.signal.aborted)).toBe(true));
});

test('首次503显示归一错误和请求编号，只在明确重试后重新读取', async () => {
  const requests: Request[] = [];
  wrapper(<MarketPanel taskId={7} />, async (request) => {
    requests.push(request);
    return Response.json({ error: { code: 'web.network_failed', args: {} }, fields: [], request_id: 'market-test-request' }, { status: 503 });
  });
  await screen.findByRole('heading', { name: '行情暂时无法读取' });
  expect(screen.getByRole('alert').textContent).toContain('market-test-request');
  expect(requests).toHaveLength(4);
  await userEvent.setup().click(screen.getByRole('button', { name: '重试' }));
  await waitFor(() => expect(requests).toHaveLength(8));
  expect(requests.every((request) => request.method === 'GET')).toBe(true);
});

test('正式服务商列表为空时隐藏解读，包括保留在库里的旧解读', async () => {
  const requests: Request[] = [];
  const old = data();
  old.insight = { id: 1, task_id: 7, generated_at: 1794297600000, stats: null, text: '不应显示的旧解读', provider_id: null, model: 'old-model' };
  wrapper(<MarketPanel taskId={7} />, marketFetch(() => Response.json([]), old, requests));
  await screen.findByRole('heading', { name: '市场行情' });
  await waitFor(() => expect(screen.queryByText('正在加载')).toBeNull());
  expect(screen.queryByRole('heading', { name: 'AI 行情解读' })).toBeNull();
  expect(screen.queryByText('不应显示的旧解读')).toBeNull();
  expect(requests.some((request) => new URL(request.url).pathname === '/api/v1/ai/providers')).toBe(true);
  expect(requests.every((request) => request.method === 'GET')).toBe(true);
});

test('有真实服务商但解读为空时显示等待区，停用或报错不等于未配置', async () => {
  wrapper(<MarketPanel taskId={7} />, marketFetch(() => Response.json([provider()])));
  await screen.findByRole('heading', { name: 'AI 行情解读' });
  expect(screen.getByText('还没有行情解读')).toBeDefined();
  expect(screen.getByText('任务成功运行并满足样本和时间条件后自动生成。')).toBeDefined();
  expect(screen.queryByRole('button', { name: /生成/ })).toBeNull();
});

test('服务商读取失败保留行情但不猜配置，明确重试成功后显示已有解读', async () => {
  let reads = 0;
  const requests: Request[] = [];
  const current = data();
  current.insight = { id: 1, task_id: 7, generated_at: 1794297600000, stats: null, text: '真实保存的行情解读', provider_id: 1, model: 'local-vision' };
  wrapper(<MarketPanel taskId={7} />, marketFetch(() => {
    reads++;
    return reads === 1 ? Response.json({ error: { code: 'web.network_failed', args: {} }, fields: [], request_id: 'providers-test' }, { status: 503 }) : Response.json([provider()]);
  }, current, requests));
  await screen.findByText('无法连接服务端，请检查网络后重试。');
  expect(screen.getByText('¥1,900.00')).toBeDefined();
  expect(screen.queryByRole('heading', { name: 'AI 行情解读' })).toBeNull();
  await userEvent.setup().click(screen.getByRole('button', { name: '重试' }));
  await screen.findByText('真实保存的行情解读');
  expect(reads).toBe(2);
  expect(requests.every((request) => request.method === 'GET')).toBe(true);
});
