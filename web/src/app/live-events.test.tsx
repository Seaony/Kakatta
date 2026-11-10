import { expect, test } from 'bun:test';
import { act, renderHook, waitFor } from '@testing-library/react';
import { QueryClientProvider, useQuery } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { apiData, ApiProvider } from '../api/hooks';
import { createApiClient } from '../api/client';
import { apiKey } from '../api/keys';
import { createAppQueryClient } from './query-client';
import { useLiveEvents } from './live-events';

class LocalEventStream extends EventTarget {
  static instances: LocalEventStream[] = [];
  closed = false;
  onopen: ((event: Event) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  constructor(readonly url: string) { super(); LocalEventStream.instances.push(this); }
  close() { this.closed = true; }
}

for (const event of ['ai.providers.changed', 'ai.usage.changed', 'ai.prompts.changed'] as const) test(`${event}重读活跃查询及相关状态，保留本地草稿且不发写请求`, async () => {
  const previous = Object.getOwnPropertyDescriptor(globalThis, 'EventSource');
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: LocalEventStream });
  LocalEventStream.instances = [];
  const queryClient = createAppQueryClient();
  const keys = {
    providers: apiKey('ai-providers'),
    budget: apiKey('ai-usage', 'budget'), usageToday: apiKey('ai-usage', 'report', 'today'), usageWeek: apiKey('ai-usage', 'report', '7d'), usageMonth: apiKey('ai-usage', 'report', '30d'),
    taskList: apiKey('tasks', 'list', { limit: 200 }), taskDetail: apiKey('tasks', 'detail', 17),
    overview: apiKey('overview', 'today'), overviewWeek: apiKey('overview', '7d'), systemStatus: apiKey('system-status'),
    settings: apiKey('settings'), prompts: apiKey('prompts'), securityTokens: apiKey('security-tokens'),
    draft: ['local-draft', 'ai-config'],
  };
  const primary = event === 'ai.providers.changed' ? keys.providers : event === 'ai.usage.changed' ? keys.budget : keys.prompts;
  const affected = new Set(event === 'ai.prompts.changed' ? ['prompts'] : [
    'taskList', 'taskDetail', 'overview', 'overviewWeek', 'systemStatus',
    ...(event === 'ai.providers.changed' ? ['providers'] : ['budget', 'usageToday', 'usageWeek', 'usageMonth']),
  ]);
  const draft = { name: '未保存的服务商名称', model: '用户手填模型' };
  for (const key of Object.values(keys)) if (key !== primary) queryClient.setQueryData(key, key === keys.draft ? draft : { fixture: true });
  const calls: string[] = [];
  const api = createApiClient({ fetch: async (request) => {
    calls.push(request.method + ' ' + new URL(request.url).pathname);
    return event === 'ai.usage.changed' ? Response.json({ limit: 0, protected_used: calls.length, protected_start: 0, effective_now: 1, next_check_at: null, blocked: false }) : Response.json([]);
  } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={queryClient}><ApiProvider api={api}>{children}</ApiProvider></QueryClientProvider>;
  const hook = renderHook(() => {
    useLiveEvents(true);
    const [text, setText] = useState('原草稿');
    const query = useQuery({ queryKey: primary, staleTime: Infinity, queryFn: async ({ signal }) => event === 'ai.providers.changed'
      ? apiData(await api.GET('/api/v1/ai/providers', { signal }))
      : event === 'ai.usage.changed' ? apiData(await api.GET('/api/v1/ai/budget', { signal }))
      : apiData(await api.GET('/api/v1/prompts', { signal })) });
    return { text, setText, query };
  }, { wrapper });
  try {
    await waitFor(() => expect(hook.result.current.query.isSuccess).toBe(true));
    act(() => hook.result.current.setText('编辑中的服务商配置'));
    expect(LocalEventStream.instances).toHaveLength(1);
    const stream = LocalEventStream.instances[0]; if (!stream) throw new Error('未打开事件连接');
    act(() => stream.dispatchEvent(new MessageEvent(event, { data: JSON.stringify(event === 'ai.providers.changed' ? { provider_id: 3 } : event === 'ai.usage.changed' ? { provider_id: 3, task_id: 17 } : { kind: 'analysis' }) })));
    await waitFor(() => expect(calls).toHaveLength(2));
    await waitFor(() => expect(hook.result.current.query.isFetching).toBe(false));
    for (const [name, key] of Object.entries(keys)) if (key !== primary) expect(queryClient.getQueryState(key)?.isInvalidated).toBe(affected.has(name));
    const read = event === 'ai.providers.changed' ? 'GET /api/v1/ai/providers' : event === 'ai.usage.changed' ? 'GET /api/v1/ai/budget' : 'GET /api/v1/prompts';
    expect(calls).toEqual([read, read]);
    expect(hook.result.current.text).toBe('编辑中的服务商配置'); expect(queryClient.getQueryData<typeof draft>(keys.draft)).toEqual(draft);
  } finally {
    hook.unmount(); queryClient.clear();
    expect(LocalEventStream.instances[0]?.closed).toBe(true);
    if (previous) Object.defineProperty(globalThis, 'EventSource', previous); else Reflect.deleteProperty(globalThis, 'EventSource');
  }
});

for (const event of ['settings.changed', 'update.changed'] as const) test(`${event}重读实际设置或更新状态，保留草稿且不重发预检`, async () => {
  const previous = Object.getOwnPropertyDescriptor(globalThis, 'EventSource');
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: LocalEventStream });
  LocalEventStream.instances = [];
  const queryClient = createAppQueryClient();
  const settingsKey = apiKey('settings'), updateKey = apiKey('update-status');
  const previewKey = apiKey('findings', 'reanalysis-preview', [7]);
  const derivedKeys = [
    apiKey('me'), apiKey('tasks', 'defaults'), apiKey('tasks', 3, 'market'), apiKey('accounts', 'detail', 3),
    apiKey('overview', 'today'), apiKey('ai-usage', 'report', 'today'), apiKey('ai-usage', 'budget'), apiKey('system-status'), apiKey('findings', 'detail', 7, {}),
  ];
  const otherKeys = [apiKey('prompts'), apiKey('setting-definitions'), apiKey('findings', 'list', {}), previewKey];
  const draftKey = ['local-draft', 'settings'];
  for (const key of [...derivedKeys, ...otherKeys, draftKey]) queryClient.setQueryData(key, { text: '原有输入' });
  const calls: string[] = [];
  let enabled = true, checkedAt = 10;
  const api = createApiClient({ fetch: async (request) => {
    const path = new URL(request.url).pathname; calls.push(request.method + ' ' + path);
    if (request.method === 'GET' && path === '/api/v1/settings') return Response.json({ values: { 'general.update_check': enabled }, sources: {}, warnings: [] });
    if (request.method === 'GET' && path === '/api/v1/system/update') return Response.json({ current_version: '1.0.0', enabled, last_attempt_at: checkedAt, last_success_at: checkedAt, next_check_at: enabled ? 1000 : null, known_release: null, update_available: null, stale: false, error: null });
    throw new Error('事件重读发出了未声明的请求');
  } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={queryClient}><ApiProvider api={api}>{children}</ApiProvider></QueryClientProvider>;
  const hook = renderHook(() => {
    useLiveEvents(true);
    const [text, setText] = useState('原输入');
    const settings = useQuery({ queryKey: settingsKey, staleTime: Infinity, queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings', { signal })) });
    const status = useQuery({ queryKey: updateKey, staleTime: Infinity, queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/system/update', { signal })) });
    useQuery({ queryKey: previewKey, staleTime: Infinity, queryFn: async ({ signal }) => apiData(await api.POST('/api/v1/findings/reanalysis-preview', { body: { ids: [7] }, signal })) });
    return { settings, status, text, setText };
  }, { wrapper });
  try {
    await waitFor(() => { expect(hook.result.current.settings.data?.values['general.update_check']).toBe(true); expect(hook.result.current.status.data?.last_success_at).toBe(10); });
    act(() => hook.result.current.setText('还没保存的设置'));
    enabled = event !== 'settings.changed'; checkedAt = 20;
    const stream = LocalEventStream.instances[0]; if (!stream) throw new Error('未打开事件连接');
    act(() => stream.dispatchEvent(new MessageEvent(event)));
    await waitFor(() => { expect(hook.result.current.status.data?.last_success_at).toBe(20); expect(hook.result.current.settings.data?.values['general.update_check']).toBe(enabled); });
    expect(hook.result.current.status.data?.enabled).toBe(enabled);
    for (const key of derivedKeys) expect(queryClient.getQueryState(key)?.isInvalidated).toBe(event === 'settings.changed');
    for (const key of [...otherKeys, draftKey]) expect(queryClient.getQueryState(key)?.isInvalidated).toBe(false);
    expect(calls.sort()).toEqual([
      'GET /api/v1/settings', ...(event === 'settings.changed' ? ['GET /api/v1/settings'] : []), 'GET /api/v1/system/update', 'GET /api/v1/system/update',
    ].sort());
    expect(hook.result.current.text).toBe('还没保存的设置');
    expect(queryClient.getQueryData<{ text: string }>(draftKey)).toEqual({ text: '原有输入' });
  } finally {
    hook.unmount(); queryClient.clear();
    if (previous) Object.defineProperty(globalThis, 'EventSource', previous); else Reflect.deleteProperty(globalThis, 'EventSource');
  }
});

test('单一事件流只失效API查询，reset重读，失效或卸载关闭连接', async () => {
  const previous = Object.getOwnPropertyDescriptor(globalThis, 'EventSource');
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: LocalEventStream });
  LocalEventStream.instances = [];
  const queryClient = createAppQueryClient();
  const api = createApiClient({ fetch: async () => { throw new Error('事件失效不应自行调用写接口'); } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={queryClient}><ApiProvider api={api}>{children}</ApiProvider></QueryClientProvider>;
  const hook = renderHook(({ enabled }) => useLiveEvents(enabled), { wrapper, initialProps: { enabled: false } });
  try {
    expect(LocalEventStream.instances).toHaveLength(0);
    queryClient.setQueryData(apiKey('tasks', 7, 'market'), { actual: true });
    queryClient.setQueryData(apiKey('security-tokens'), []);
    queryClient.setQueryData(['local-draft'], { text: 'keep' });
    hook.rerender({ enabled: true });
    expect(LocalEventStream.instances).toHaveLength(1);
    const stream = LocalEventStream.instances[0];
    if (!stream) throw new Error('未打开事件连接');
    expect(stream.url).toBe('/api/v1/events');
    stream.dispatchEvent(new Event('task.changed'));
    await waitFor(() => expect(queryClient.getQueryState(apiKey('tasks', 7, 'market'))?.isInvalidated).toBe(true));
    expect(queryClient.getQueryState(apiKey('security-tokens'))?.isInvalidated).toBe(false);
    stream.dispatchEvent(new Event('reset'));
    expect(queryClient.getQueryState(apiKey('security-tokens'))?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(['local-draft'])?.isInvalidated).toBe(false);
    hook.rerender({ enabled: false });
    expect(stream.closed).toBe(true);
  } finally {
    hook.unmount(); queryClient.clear();
    if (previous) Object.defineProperty(globalThis, 'EventSource', previous); else Reflect.deleteProperty(globalThis, 'EventSource');
  }
});

test('断流只读核身份一次，不能直接当作登出；重连后允许重新核对', async () => {
  const previous = Object.getOwnPropertyDescriptor(globalThis, 'EventSource');
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: LocalEventStream });
  LocalEventStream.instances = [];
  const queryClient = createAppQueryClient();
  let calls = 0, expired = false, lost = 0;
  const api = createApiClient({ fetch: async (request) => {
    expect(request.method).toBe('GET');
    expect(new URL(request.url).pathname).toBe('/api/v1/me');
    calls++;
    return expired ? Response.json({ error: { code: 'auth.unauthenticated', args: {} }, fields: [], request_id: 'local' }, { status: 401 }) : Response.json({ username: 'owner' });
  } });
  api.subscribeAuthenticationLost(() => { lost++; });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={queryClient}><ApiProvider api={api}>{children}</ApiProvider></QueryClientProvider>;
  const hook = renderHook(() => useLiveEvents(true), { wrapper });
  try {
    const stream = LocalEventStream.instances[0];
    if (!stream) throw new Error('未打开事件连接');
    stream.onerror?.(new Event('error'));
    stream.onerror?.(new Event('error'));
    await waitFor(() => expect(calls).toBe(1));
    expect(lost).toBe(0);
    expired = true;
    stream.onopen?.(new Event('open'));
    stream.onerror?.(new Event('error'));
    await waitFor(() => expect(lost).toBe(1));
    expect(calls).toBe(2);
  } finally {
    hook.unmount(); queryClient.clear();
    if (previous) Object.defineProperty(globalThis, 'EventSource', previous); else Reflect.deleteProperty(globalThis, 'EventSource');
  }
});
