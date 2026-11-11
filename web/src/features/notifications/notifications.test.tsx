import { describe, expect, mock, test } from 'bun:test';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { useEffect, useState, type ReactNode } from 'react';
import { createApiClient, type ApiFetch } from '../../api/client';
import { ApiProvider, useApi } from '../../api/hooks';
import { ApiError } from '../../api/errors';
import { AuthenticationContext } from '../../app/auth-state';
import type { components } from '../../api/schema.gen';
import { createI18n, I18nProvider, type Locale } from '../../i18n';
import { ChannelSetup, TaskChannelPicker } from './channels';
import { ChannelEditor } from './channel-editor';
import { NotificationsPage, readNotificationRules } from './rules';
import { channelKinds } from './catalog';
import { newChannelDraft, validateChannelDraft } from './draft';
import { channelRow, createChannelAdapter } from './adapter';
import type { ChannelAdapter, ChannelDraft, ChannelRow } from './types';

const snapshot: components['schemas']['SettingsSnapshot'] = { values: { 'notify.link_target': 'mobile', 'notify.max_per_run': 5, 'notify.system_channel_ids': [], 'general.timezone': 'Asia/Shanghai', 'general.public_url': '' }, sources: {}, warnings: [] };
function channel(id = 1, name = '本地假机器人'): ChannelRow { return { id, name, kind: 'telegram', enabled: true, fields: { chat_id: 'fixture-chat', api_url: '' }, hasSecret: { token: true }, testedAt: null, testOK: null, testError: null }; }
function fixture(initial: ChannelRow[] = []) {
  let rows = initial;
  const list = mock(async (_signal: AbortSignal) => rows);
  const save = mock(async (id: number | null, draft: ChannelDraft, _signal: AbortSignal) => { const saved: ChannelRow = { ...channel(id ?? 3, draft.name), kind: draft.kind, enabled: draft.enabled, fields: draft.fields }; rows = [...rows.filter((row) => row.id !== saved.id), saved]; return saved; });
  const send = mock(async (id: number, _signal: AbortSignal) => ({ channel_id: id, delivery_id: 4, at: 1700000000000, ok: true, error: null }));
  const history = mock(async (_id: number, _signal: AbortSignal): Promise<components['schemas']['DeliverySummary'][]> => []);
  const deliveryItems = mock(async (_id: number, _signal: AbortSignal) => []);
  const adapter: ChannelAdapter = { get: async (id) => rows.find((row) => row.id === id) ?? channel(id), list, save, test: send, history, deliveryItems, setEnabled: async (id, enabled) => { const saved = { ...channel(id), enabled }; rows = rows.map((row) => row.id === id ? saved : row); return saved; }, remove: async (id) => { rows = rows.filter((row) => row.id !== id); } };
  return { adapter, list, save, send, history, deliveryItems };
}
const defaultFetch: ApiFetch = async (request) => { const path = new URL(request.url).pathname; if (path === '/api/v1/channels') return Response.json([]); if (path === '/api/v1/settings') return Response.json(snapshot); if (path === '/api/v1/settings/definitions') return Response.json([]); throw new Error('测试禁止访问未声明地址'); };
async function mount(element: ReactNode, fetch: ApiFetch = defaultFetch, locale: Locale = 'zh-CN') {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } } }); const api = createApiClient({ fetch });
  const root = createRootRoute({ component: () => element }); const router = createRouter({ routeTree: root, history: createMemoryHistory({ initialEntries: ['/'] }) });
  await act(async () => { await router.load(); });
  return render(<I18nProvider instance={createI18n(locale)}><ApiProvider api={api}><QueryClientProvider client={queryClient}><RouterProvider router={router} /></QueryClientProvider></ApiProvider></I18nProvider>);
}

describe('通知表单草稿', () => {
  test('11类型有独立草稿，已存秘密不回填，切类型不继承秘密', () => { expect(channelKinds).toHaveLength(11); expect(newChannelDraft('telegram', channel()).secrets.token).toEqual({ mode: 'keep', value: '' }); expect(newChannelDraft('bark').secrets.token).toBeUndefined(); for (const kind of channelKinds) expect(newChannelDraft(kind).kind).toBe(kind); });
  test('Webhook按JSON结构校验，FORM要求对象，GET保留原输入', () => { const draft = newChannelDraft('webhook'); draft.name = '本地Webhook'; draft.secrets.url = { mode: 'replace', value: 'http://127.0.0.1/fixture' }; draft.fields.body_template = '{broken'; expect(validateChannelDraft(draft).body_template).toBe('json'); draft.fields.body_template = '["{{title}}"]'; draft.fields.content_type = 'FORM'; expect(validateChannelDraft(draft).body_template).toBe('object'); draft.fields.method = 'GET'; expect(validateChannelDraft(draft).body_template).toBeUndefined(); expect(draft.fields.body_template).toBe('["{{title}}"]'); });
  test('未知和无效通知设置不伪装成默认值', () => { expect(() => readNotificationRules({ ...snapshot, values: {} })).toThrow(); expect(() => readNotificationRules({ ...snapshot, values: { ...snapshot.values, 'notify.max_per_run': 0 } })).toThrow(); expect(readNotificationRules(snapshot)).toEqual({ link: 'mobile', maximum: '5', all: true, ids: [] }); });
});

describe('通知组件', () => {
  test('完整11类型使用正式保存入口，非法空表单不假保存', async () => { const saved = mock(() => {}); await mount(<ChannelSetup onSaved={saved} />); await userEvent.setup().click(screen.getByRole('button', { name: '添加渠道' })); expect(within(screen.getByRole('combobox', { name: '渠道类型' })).getAllByRole('option')).toHaveLength(11); await userEvent.setup().click(screen.getByRole('button', { name: '保存渠道' })); expect(screen.getAllByText('请填写这一项。').length).toBeGreaterThan(0); expect(saved).not.toHaveBeenCalled(); });
  for (const [locale, label, help] of [
    ['zh-CN', '渠道类型', '钉钉逐步限制新建自定义机器人，已有机器人仍可使用。关键词安全设置请填 Kakatta。同一目标至少间隔 3 秒。'],
    ['en', 'Channel type', 'DingTalk restricts new custom bots; existing bots can still be used. Use Kakatta for keyword security. Deliveries to one target are spaced at least 3 seconds apart.'],
  ] as const) test(`钉钉设置实际显示机器人限制和安全关键词说明（${locale}）`, async () => {
    const f = fixture();
    await mount(<ChannelEditor channel={null} adapter={f.adapter} onSaved={() => {}} onCancel={() => {}} />, defaultFetch, locale);
    await userEvent.setup().selectOptions(screen.getByRole('combobox', { name: label }), 'dingtalk');
    expect(screen.getByText(help)).toBeDefined();
    expect(f.save).not.toHaveBeenCalled(); expect(f.send).not.toHaveBeenCalled();
  });
  test('保存失败保留新密钥，确认后才回调且不自动发送测试', async () => {
    const f = fixture(); const saved = mock(() => {}); f.save.mockImplementationOnce(async () => { throw new ApiError(422, { code: 'settings.invalid', args: {} }, [{ key: 'secrets.token', reason: { code: 'notify.error.invalid_config', args: {} } }], 'fixture-request'); });
    await mount(<ChannelEditor channel={channel()} adapter={f.adapter} onSaved={saved} onCancel={() => {}} />); const user = userEvent.setup(); await user.click(screen.getByRole('radio', { name: '重新填写' })); const input = screen.getByLabelText('访问令牌', { selector: 'input' }); await user.type(input, 'fixture-secret'); await user.click(screen.getByRole('button', { name: '保存渠道' }));
    await screen.findByText('请求编号：fixture-request'); expect((input as HTMLInputElement).value).toBe('fixture-secret'); expect(saved).not.toHaveBeenCalled(); await user.click(screen.getByRole('button', { name: '保存渠道' })); await waitFor(() => expect(saved).toHaveBeenCalledTimes(1)); expect(f.save.mock.calls[1]?.[1].secrets.token).toEqual({ mode: 'replace', value: 'fixture-secret' }); expect(f.send).not.toHaveBeenCalled();
  });
  test('同类型多个机器人按实例独立多选', async () => { const f = fixture([channel(1, '群甲'), channel(2, '群乙')]); const changed = mock((_ids: number[]) => {}); function Picker() { const [ids, setIDs] = useState([1]); return <TaskChannelPicker adapter={f.adapter} value={ids} onChange={(next) => { changed(next); setIDs(next); }} />; } await mount(<Picker />); const user = userEvent.setup(); await user.click(await screen.findByRole('checkbox', { name: '群乙' })); expect(changed).toHaveBeenLastCalledWith([1, 2]); await user.click(screen.getByRole('checkbox', { name: '群甲' })); expect(changed).toHaveBeenLastCalledWith([2]); });
  test('测试显式触发且禁止连点，断线不自动重发', async () => {
    const f = fixture([channel()]); let finish: (() => void) | undefined; f.send.mockImplementationOnce((id) => new Promise((resolve) => { finish = () => resolve({ channel_id: id, delivery_id: 4, at: 1700000000000, ok: true, error: null }); })); await mount(<ChannelSetup adapter={f.adapter} />); const user = userEvent.setup(); const button = await screen.findByRole('button', { name: '向 本地假机器人 发送测试' }); expect(f.send).not.toHaveBeenCalled(); await user.dblClick(button); expect(f.send).toHaveBeenCalledTimes(1); expect((button as HTMLButtonElement).disabled).toBe(true); await act(async () => finish?.()); await screen.findByText('发送成功'); f.send.mockImplementationOnce(async () => { throw new Error('连接中断'); }); await user.click(button); await screen.findByText('发送结果还未确认，请先刷新发送记录。'); expect((button as HTMLButtonElement).disabled).toBe(true); expect(f.send).toHaveBeenCalledTimes(2);
  });
  test('非法JSON保留并阻止提交，GET不丢模板，组合输入不提交', async () => {
    const f = fixture(); await mount(<ChannelEditor channel={null} adapter={f.adapter} onSaved={() => {}} onCancel={() => {}} />); const user = userEvent.setup(); await user.selectOptions(screen.getByRole('combobox', { name: '渠道类型' }), 'webhook'); await user.type(screen.getByRole('textbox', { name: '名称' }), '假Webhook'); await user.type(screen.getByLabelText('Webhook 地址'), 'http://127.0.0.1/fixture'); const area = screen.getByRole('textbox', { name: '请求体模板 JSON（可选）' }); fireEvent.change(area, { target: { value: '{bad' } }); await user.click(screen.getByRole('button', { name: '保存渠道' })); await screen.findByText('请输入有效的 JSON。'); expect(f.save).not.toHaveBeenCalled(); await user.selectOptions(screen.getByRole('combobox', { name: '请求方法' }), 'GET'); expect((area as HTMLTextAreaElement).value).toBe('{bad'); expect((area as HTMLTextAreaElement).disabled).toBe(true); fireEvent.keyDown(screen.getByRole('textbox', { name: '名称' }), { key: 'Enter', isComposing: true }); expect(f.save).not.toHaveBeenCalled();
  });
  test('Webhook服务端字段错误保留JSON原文，401清理新秘密且不重复提交', async () => {
    const query = '{"id":9007199254740993,"nested":{"exponent":1.234567890123456789e30}}';
    const body = ' {"id":9007199254740993,"nested":[1e999,1e-999]}\n';
    const webhook: components['schemas']['ChannelWebhook'] = { id: 9, type: 'webhook', name: '原文Webhook', enabled: true, config: { method: 'POST', content_type: 'JSON', query_params: query, body_template: body }, secret_set: { url: false, headers: false }, last_test_at: null, last_test_ok: null, last_error: null };
    const saved = mock(() => {}); const requests: unknown[] = [];
    const fetch: ApiFetch = async (request) => {
      expect(request.method).toBe('PATCH'); requests.push(await request.json());
      return requests.length === 1
        ? Response.json({ error: { code: 'settings.invalid', args: {} }, fields: [{ key: 'config.body_template', reason: { code: 'notify.error.invalid_config', args: {} } }], request_id: 'webhook-field' }, { status: 422 })
        : Response.json({ error: { code: 'auth.unauthenticated', args: {} }, fields: [], request_id: 'webhook-auth' }, { status: 401 });
    };
    function Example() {
      const api = useApi(); const [authenticated, setAuthenticated] = useState(true);
      useEffect(() => api.subscribeAuthenticationLost(() => setAuthenticated(false)), [api]);
      return <AuthenticationContext.Provider value={{ authenticated, user: undefined, complete: () => {}, refresh: () => {} }}><ChannelEditor channel={channelRow(webhook)} adapter={createChannelAdapter(api)} onSaved={saved} onCancel={() => {}} /></AuthenticationContext.Provider>;
    }
    await mount(<Example />, fetch);
    const user = userEvent.setup(); const url = screen.getByLabelText<HTMLInputElement>('Webhook 地址'); const headers = screen.getByRole<HTMLTextAreaElement>('textbox', { name: '请求头 JSON（可选）' });
    const bodyInput = screen.getByRole<HTMLTextAreaElement>('textbox', { name: '请求体模板 JSON（可选）' }); const queryInput = screen.getByRole<HTMLTextAreaElement>('textbox', { name: '查询参数 JSON（可选）' });
    fireEvent.change(url, { target: { value: 'http://fixture.invalid/private' } }); fireEvent.change(headers, { target: { value: '{"Authorization":"fixture-secret"}' } });
    await user.click(screen.getByRole('button', { name: '保存渠道' })); await screen.findByText('请求编号：webhook-field');
    expect(bodyInput.getAttribute('aria-invalid')).toBe('true'); expect(bodyInput.value).toBe(body); expect(queryInput.value).toBe(query); expect(headers.value).toBe('{"Authorization":"fixture-secret"}'); expect(url.value).toBe('http://fixture.invalid/private');
    expect(requests[0]).toEqual({ name: webhook.name, enabled: true, type: 'webhook', config: { method: 'POST', content_type: 'JSON', query_params: query, body_template: body }, secrets: { url: 'http://fixture.invalid/private', headers: { Authorization: 'fixture-secret' } } });
    await user.click(screen.getByRole('button', { name: '保存渠道' }));
    await waitFor(() => { expect(url.value).toBe(''); expect(headers.value).toBe(''); });
    expect(bodyInput.value).toBe(body); expect(queryInput.value).toBe(query); expect(screen.getByRole<HTMLInputElement>('textbox', { name: '名称' }).value).toBe(webhook.name); expect(requests[1]).toEqual(requests[0]); expect(saved).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: '保存渠道' })); expect(requests).toHaveLength(2);
  });
  test('近20次失败保留完整商品数，按需读关联商品，清理项不冒充新商品', async () => {
    const f = fixture([channel()]); f.history.mockImplementationOnce(async () => Array.from({ length: 20 }, (_, index) => ({ id: index + 1, at: null, channel_id: 1, channel_name: '本地假机器人', task_id: null, kind: index === 0 ? 'summary' : 'test', item_count: index === 0 ? 13 : 0, ok: index !== 0, error: index === 0 ? { code: 'notify.error.http_status', args: { status: 401 } } : null })));
    await mount(<ChannelSetup adapter={f.adapter} />); const user = userEvent.setup(); await user.click(await screen.findByRole('button', { name: '最近 20 次发送' })); const dialog = await screen.findByRole('dialog', { name: '本地假机器人：最近 20 次发送' }); await waitFor(() => expect(within(dialog).getAllByRole('listitem')).toHaveLength(20)); expect(within(dialog).getByText('13')).toBeDefined(); expect(f.deliveryItems).not.toHaveBeenCalled(); await user.click(within(dialog).getByRole('button', { name: '查看包含的商品' })); await within(dialog).findByText('商品记录已清理，发送记录仍保留。'); expect(f.deliveryItems).toHaveBeenCalledTimes(1); expect(within(dialog).getByText('13')).toBeDefined(); expect(f.send).not.toHaveBeenCalled();
  });
  test('会话失效清掉敏感草稿但保留名称，重新登录前不保留新秘密', async () => {
    const f = fixture(); function Example() { const [authenticated, setAuthenticated] = useState(true); return <AuthenticationContext.Provider value={{ authenticated, user: undefined, complete: () => {}, refresh: () => {} }}><button onClick={() => setAuthenticated(false)}>测试失效</button><ChannelEditor channel={null} adapter={f.adapter} onSaved={() => {}} onCancel={() => {}} /></AuthenticationContext.Provider>; }
    await mount(<Example />); const user = userEvent.setup(); await user.type(screen.getByRole('textbox', { name: '名称' }), '保留名称'); const secret = screen.getByLabelText('推送地址'); await user.type(secret, 'https://fixture.invalid/secret'); await user.click(screen.getByRole('button', { name: '测试失效' })); expect((secret as HTMLInputElement).value).toBe(''); expect((screen.getByRole('textbox', { name: '名称' }) as HTMLInputElement).value).toBe('保留名称'); expect(f.save).not.toHaveBeenCalled();
  });
  test('离开取消列表读取，不把取消当空渠道，不重试请求', async () => {
    const f = fixture(); let observed: AbortSignal | undefined; f.list.mockImplementationOnce((signal) => { observed = signal; return new Promise((_resolve, reject) => signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')))); }); const availability = mock((_value: boolean | null) => {}); const view = await mount(<ChannelSetup adapter={f.adapter} onAvailabilityChange={availability} />); await waitFor(() => expect(observed).toBeDefined()); view.unmount(); await waitFor(() => expect(observed?.aborted).toBe(true)); expect(f.list).toHaveBeenCalledTimes(1); expect(availability.mock.calls.every(([value]) => value === null)).toBe(true);
  });
  test('真实settings只提交变更键；失败保输入；系统空列表仍为全启用', async () => {
    const patches: unknown[] = []; let fail = true; const fetch: ApiFetch = async (request) => { if (request.method !== 'PATCH') return defaultFetch(request); patches.push(await request.json()); if (fail) return Response.json({ error: { code: 'settings.invalid', args: {} }, fields: [], request_id: 'rule-fixture' }, { status: 422 }); return Response.json({ ...snapshot, values: { ...snapshot.values, 'notify.link_target': 'desktop', 'notify.max_per_run': 1 } }); };
    await mount(<NotificationsPage />, fetch); const user = userEvent.setup(); await user.click(await screen.findByRole('radio', { name: '网页版' })); const max = screen.getByRole('textbox', { name: '每次推送总数上限' }); await user.clear(max); await user.type(max, '1'); await user.click(screen.getByRole('button', { name: '保存通知规则' })); await screen.findByText('请求编号：rule-fixture'); expect((max as HTMLInputElement).value).toBe('1'); expect(patches[0]).toEqual({ values: { 'notify.link_target': 'desktop', 'notify.max_per_run': 1 } }); fail = false; await user.click(screen.getByRole('button', { name: '保存通知规则' })); await screen.findByText('通知规则已保存。'); expect(patches).toHaveLength(2); expect(screen.getByRole('radio', { name: '所有已启用渠道（默认）' }).getAttribute('data-state')).toBe('checked');
  });
});

describe('通知页操作层级和说明', () => {
  test.each(['zh-CN', 'en', 'ja'] as const)('渠道删除弱化，任务通知条件只解释一次，确认删除仍醒目（%s）', async (locale) => {
    const i18n = createI18n(locale);
    const f = fixture([channel(1, '群甲'), channel(2, '群乙')]);
    await mount(<NotificationsPage adapter={f.adapter} />, async (request) => new URL(request.url).pathname === '/api/v1/tasks' ? Response.json({ items: [], next_cursor: null, total: 0 }) : defaultFetch(request), locale);
    await screen.findByRole('heading', { name: '群甲' });
    expect(screen.getAllByText(i18n.t('web.notify.test_eligibility'))).toHaveLength(1);
    const remove = screen.getAllByRole('button', { name: i18n.t('web.notify.delete') });
    expect(remove).toHaveLength(2);
    for (const button of remove) {
      expect(button.classList.contains('bg-transparent')).toBe(true);
      expect(button.classList.contains('bg-danger')).toBe(false);
    }
    await userEvent.setup().click(remove[0]!);
    const dialog = await screen.findByRole('alertdialog', { name: i18n.t('web.notify.delete_named', { name: '群甲' }) });
    const confirm = await within(dialog).findByRole('button', { name: i18n.t('web.notify.delete') });
    await waitFor(() => expect((confirm as HTMLButtonElement).disabled).toBe(false));
    expect(confirm.classList.contains('bg-danger')).toBe(true);
  });
});
