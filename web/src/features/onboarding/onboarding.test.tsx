import { expect, test } from 'bun:test';
import { QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createApiClient } from '../../api/client';
import type { ApiFetch } from '../../api/client';
import { ApiProvider } from '../../api/hooks';
import type { Schema } from '../../api/types';
import { createAppQueryClient } from '../../app/query-client';
import { createI18n, I18nProvider } from '../../i18n';
import { OnboardingPage } from './onboarding';

const task: Schema<'TaskView'> = {
  id: 11, name: '练琴用电钢琴', keyword: '电钢琴', enabled: true, account_id: null, account_pinned: false,
  min_price: null, max_price: null, personal_only: true, free_shipping_only: false, publish_window: 'any',
  region_province: null, region_city: null, region_district: null, max_pages: 1, interval_minutes: 30,
  crawl_overrides: {}, judge_mode: 'keyword', requirement: '', profile: {}, budget_min: null, budget_max: null,
  match_keywords: [], exclude_keywords: [], analyze_on_first_run: true, notify_policy: 'recommended',
  notify_price_drop: true, channel_ids: [], search_generation: 1, baseline_pending: true, sort_index: 0,
  created_at: 1_762_800_000_000, updated_at: 1_762_800_000_000, last_run_at: null, last_success_at: null,
  next_run_at: 1_762_800_000_000, last_run_outcome: null, last_error: null, consecutive_failures: 0,
  failure_paused_until: null, no_good_hint_dismissed_at: null, finding_counts: { total: 0 }, no_good_hint: null,
  runtime: { task_id: 11, phase: 'idle', run_id: null, account_id: null, queued_at: null, started_at: null, blocked: null },
};

function snapshot(done: boolean): Schema<'SettingsSnapshot'> {
  return { values: { 'general.onboarding_done': done }, sources: { 'general.onboarding_done': 'stored' }, warnings: [] };
}

function unavailable() {
  return Response.json({ error: { code: 'http.unavailable', args: {} }, fields: [], request_id: 'request-local-92' }, { status: 503 });
}

async function mount(transport: ApiFetch, onCompleted: () => void = () => {}) {
  const queryClient = createAppQueryClient();
  const route = createRootRoute({ component: () => <OnboardingPage onCompleted={onCompleted} /> });
  const router = createRouter({ routeTree: route, history: createMemoryHistory({ initialEntries: ['/'] }) });
  await router.load();
  const view = render(<I18nProvider instance={createI18n('zh-CN')}><QueryClientProvider client={queryClient}>
    <ApiProvider api={createApiClient({ fetch: transport })}><RouterProvider router={router} /></ApiProvider>
  </QueryClientProvider></I18nProvider>);
  return { close() { view.unmount(); queryClient.clear(); } };
}

test('中断后已有任务只保存完成标记，失败不重建任务也不假成功', async () => {
  const writes: unknown[] = [];
  const requests: string[] = [];
  let completed = 0;
  const page = await mount(async (request) => {
    const path = new URL(request.url).pathname;
    requests.push(`${request.method} ${path}`);
    if (path === '/api/v1/tasks' && request.method === 'GET') {
      expect(new URL(request.url).searchParams.get('limit')).toBe('1');
      return Response.json({ items: [task], next_cursor: null, total: 1 } satisfies Schema<'PageTaskView'>);
    }
    if (path === '/api/v1/settings' && request.method === 'PATCH') {
      writes.push(await request.json());
      return writes.length === 1 ? unavailable() : Response.json(snapshot(true));
    }
    if (path === '/api/v1/settings') return Response.json(snapshot(false));
    throw new Error('不应创建任务或启动其它操作');
  }, () => { completed++; });
  try {
    await screen.findByText('已有任务，无需重复创建。');
    expect(completed).toBe(0);
    await userEvent.click(screen.getByRole('button', { name: '完成设置' }));
    await screen.findByText('服务暂时不可用，请稍后重试。');
    expect(completed).toBe(0);
    expect(writes).toEqual([{ values: { 'general.onboarding_done': true } }]);
    expect(screen.getByText('已有任务，无需重复创建。')).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: '完成设置' }));
    await waitFor(() => expect(completed).toBe(1));
    expect(writes).toHaveLength(2);
    expect(requests.filter((request) => request.startsWith('POST'))).toEqual([]);
    expect(screen.getByText('已完成首次设置。')).toBeTruthy();
  } finally { page.close(); }
});

test('完成标记已保存时不因任务为空再次强制引导', async () => {
  const requests: string[] = [];
  const page = await mount(async (request) => {
    const path = new URL(request.url).pathname;
    requests.push(path);
    return Response.json(snapshot(true));
  });
  try {
    await screen.findByText('已完成首次设置。');
    expect(requests).toEqual(['/api/v1/settings']);
    expect(screen.getByRole('link', { name: '查看任务' }).getAttribute('href')).toBe('/tasks');
    expect(screen.queryByRole('button', { name: '完成设置' })).toBeNull();
  } finally { page.close(); }
});

test('任务读取失败不能当无任务，重试读取后按真实集合恢复', async () => {
  let reads = 0;
  const page = await mount(async (request) => {
    if (new URL(request.url).pathname === '/api/v1/settings') return Response.json(snapshot(false));
    reads++;
    return reads === 1 ? unavailable() : Response.json({ items: [task], next_cursor: null, total: 1 } satisfies Schema<'PageTaskView'>);
  });
  try {
    await screen.findByText('服务暂时不可用，请稍后重试。');
    expect(reads).toBe(1);
    expect(screen.queryByRole('button', { name: '完成设置' })).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: '重试' }));
    await screen.findByText('已有任务，无需重复创建。');
    expect(reads).toBe(2);
  } finally { page.close(); }
});

test('完成响应丢失先读回设置，不重发写入且不提前回调', async () => {
  let serverDone = false;
  let writes = 0;
  let completed = 0;
  const page = await mount(async (request) => {
    if (new URL(request.url).pathname === '/api/v1/tasks') return Response.json({ items: [task], next_cursor: null, total: 1 } satisfies Schema<'PageTaskView'>);
    if (request.method === 'PATCH') { writes++; serverDone = true; throw new TypeError('local response lost'); }
    return Response.json(snapshot(serverDone));
  }, () => { completed++; });
  try {
    await screen.findByRole('button', { name: '完成设置' });
    await userEvent.click(screen.getByRole('button', { name: '完成设置' }));
    await screen.findByText('尚未确认操作结果，请先检查已保存的内容。');
    expect(completed).toBe(0);
    expect(writes).toBe(1);
    await userEvent.click(screen.getByRole('button', { name: '重新检查状态' }));
    await waitFor(() => expect(completed).toBe(1));
    expect(writes).toBe(1);
    expect(screen.getByText('已完成首次设置。')).toBeTruthy();
  } finally { page.close(); }
});

test('真实服务商表单保存停用配置后仍不能继续，启用成功才恢复继续', async () => {
  const account: Schema<'AccountView'> = {
    id: 7, name: '家用网络', nickname: null, avatar_url: null, proxy_id: null, state: 'active', state_reason: null,
    state_changed_at: null, cooldown_until: null, verify_level: 0, last_cooldown_end_at: null,
    relogin_after_pause: false,
    cooldown_manual_used: false, last_run_ended_at: null, next_gap_seconds: null, last_used_at: null,
    created_at: 1_790_000_000_000, session: null,
    quota: { hourly_used: 8, daily_search_used: 24, daily_detail_used: 12, hourly_limit: 30, daily_search_limit: 200,
      daily_detail_limit: 100, day_start_at: 1_790_000_000_000, as_of: 1_790_001_234_000 },
  };
  let provider: Schema<'ProviderView'> = {
    id: 3, name: '停用的本地模型', preset: 'custom', base_url: 'http://model.example/v1',
    vision_model: 'vision', text_model: null, api_key_set: false, enabled: false,
    sort_index: 0, error_state: null, error_at: null,
  };
  const createInput: Schema<'ProviderInput'> = {
    name: provider.name, preset: 'custom', base_url: provider.base_url,
    vision_model: provider.vision_model, text_model: null, enabled: false,
  };
  let saved = false;
  const bodies: unknown[] = [];
  const unexpected: string[] = [];
  const page = await mount(async (request) => {
    const path = new URL(request.url).pathname;
    if (path === '/api/v1/settings') return Response.json(snapshot(false));
    if (path === '/api/v1/tasks') return Response.json({ items: [], next_cursor: null, total: 0 } satisfies Schema<'PageTaskView'>);
    if (path === '/api/v1/accounts') return Response.json({ items: [account], next_cursor: null, total: 1 } satisfies Schema<'PageAccountView'>);
    if (path === '/api/v1/accounts/7') return Response.json(account);
    if (path === '/api/v1/proxies') return Response.json({ items: [], next_cursor: null, total: 0 } satisfies Schema<'PageProxyView'>);
    if (path === '/api/v1/ai/providers' && request.method === 'GET') return Response.json(saved ? [provider] : []);
    if (path === '/api/v1/ai/providers' && request.method === 'POST') {
      bodies.push(await request.json()); saved = true;
      return Response.json({ provider, warnings: [] } satisfies Schema<'ProviderSaveResult'>, { status: 201 });
    }
    if (path === '/api/v1/ai/providers/3/enabled' && request.method === 'PUT') {
      bodies.push(await request.json()); provider = { ...provider, enabled: true };
      return Response.json(provider);
    }
    unexpected.push(`${request.method} ${path}`);
    throw new Error('本次引导不应执行其它请求');
  });
  try {
    await userEvent.click(await screen.findByRole('button', { name: /家用网络/ }));
    await waitFor(() => expect(screen.getByRole('button', { name: '继续' }).hasAttribute('disabled')).toBe(false));
    await userEvent.click(screen.getByRole('button', { name: '继续' }));
    await userEvent.type(await screen.findByRole('textbox', { name: '名称' }), provider.name);
    await userEvent.type(screen.getByRole('textbox', { name: '接口地址' }), provider.base_url);
    await userEvent.type(screen.getByRole('textbox', { name: '图片模型' }), provider.vision_model);
    await userEvent.click(screen.getByRole('checkbox', { name: '启用' }));
    expect(screen.getByRole('button', { name: '继续' }).hasAttribute('disabled')).toBe(true);
    await userEvent.click(screen.getByRole('button', { name: '保存' }));
    await screen.findByRole('heading', { name: provider.name });
    expect(bodies).toEqual([createInput]);
    expect(screen.getByRole('button', { name: '继续' }).hasAttribute('disabled')).toBe(true);
    expect(screen.getByRole('button', { name: '暂时跳过 AI' }).hasAttribute('disabled')).toBe(false);
    await userEvent.click(screen.getByRole('checkbox', { name: `启用：${provider.name}` }));
    await waitFor(() => expect(screen.getByRole('button', { name: '继续' }).hasAttribute('disabled')).toBe(false));
    expect(bodies).toEqual([createInput, { enabled: true }]);
    expect(unexpected).toEqual([]);
  } finally { page.close(); }
});
