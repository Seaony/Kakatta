import { describe, expect, test } from 'bun:test';
import { createApiClient } from '../../api/client';
import type { components } from '../../api/schema.gen';
import { ApiError } from '../../api/errors';
import { channelInput, channelRow, createChannelAdapter } from './adapter';
import { channelCatalog, channelKinds } from './catalog';
import { newChannelDraft } from './draft';

const bark: components['schemas']['ChannelBark'] = { id: 1, type: 'bark', name: '假Bark', enabled: true, config: {}, secret_set: { url: true }, last_test_at: null, last_test_ok: null, last_error: null };
const telegram: components['schemas']['ChannelTelegram'] = { ...bark, id: 2, type: 'telegram', config: { chat_id: 'fixture-chat', api_url: '' }, secret_set: { token: true } };

describe('正式通知API映射', () => {
  test('11渠道生成准确分区，不把秘密写进config，端口为整数', () => {
    for (const kind of channelKinds) {
      const draft = newChannelDraft(kind); draft.name = 'fixture';
      for (const field of channelCatalog[kind].fields) {
        if (field.secret) { if (field.required) draft.secrets[field.key] = { mode: 'replace', value: field.key === 'url' ? 'http://fixture.invalid/push' : 'fixture-key' }; }
        else draft.fields[field.key] = field.options?.[0] ?? (field.numeric ? '465' : field.json ? '{}' : field.key === 'url' || field.key === 'api_url' ? 'http://fixture.invalid' : field.key === 'from' || field.key === 'to' ? 'test@fixture.invalid' : 'fixture');
      }
      const input = channelInput(draft); expect(input.type).toBe(kind); expect(input.name).toBe('fixture');
      for (const field of channelCatalog[kind].fields.filter((field) => field.secret)) expect(Object.keys(input.config ?? {})).not.toContain(field.key);
      if (input.type === 'email') expect(input.config?.port).toBe(465);
    }
  });
  test('保留省略秘密键，清空使用空串/headers对象，空模板使用默认', () => {
    const edit = newChannelDraft('telegram', channelRow(telegram)); const preserved = channelInput(edit); expect(preserved.secrets).toEqual({}); expect(JSON.stringify(preserved)).not.toContain('已设置');
    const ntfy = newChannelDraft('ntfy'); ntfy.name = 'fixture'; ntfy.secrets.url = { mode: 'replace', value: 'http://fixture.invalid/topic' }; ntfy.secrets.token = { mode: 'clear', value: '' }; expect(channelInput(ntfy).secrets).toEqual({ url: 'http://fixture.invalid/topic', token: '' });
    const webhook = newChannelDraft('webhook'); webhook.name = 'fixture'; webhook.secrets.url = { mode: 'replace', value: 'http://fixture.invalid/webhook' }; webhook.secrets.headers = { mode: 'clear', value: '' }; const input = channelInput(webhook); expect(input.secrets).toEqual({ url: 'http://fixture.invalid/webhook', headers: {} }); if (input.type === 'webhook') expect(input.config?.body_template).toBeUndefined();
  });
  test('Webhook加载、编辑和保存保持不透明JSON原文，不舍入大整数或重写指数', async () => {
    const query = ' {"external_id":9007199254740993,"nested":{"ids":[9007199254740993,1.234567890123456789e30]}}\n';
    const body = '\n{"external_id":9007199254740993,"nested":[{"n":9.007199254740993e15}],"large":1e999}\t';
    const editedBody = body.replace('"large":1e999', '"large":1e999,"small":1e-999');
    const webhook: components['schemas']['ChannelWebhook'] = { ...bark, type: 'webhook', config: { method: 'POST', content_type: 'JSON', query_params: query, body_template: body }, secret_set: { url: true, headers: true } };
    const requests: unknown[] = [];
    const api = createApiClient({ fetch: async (request) => {
      if (request.method === 'PATCH') requests.push(await request.json());
      return Response.json(webhook);
    } });
    const adapter = createChannelAdapter(api); const signal = new AbortController().signal;
    const loaded = await adapter.get(1, signal);
    expect(loaded.fields.query_params).toBe(query); expect(loaded.fields.body_template).toBe(body);
    const draft = newChannelDraft('webhook', loaded);
    expect(draft.fields.query_params).toBe(query); expect(draft.fields.body_template).toBe(body);
    await adapter.save(1, draft, signal);
    draft.fields.body_template = editedBody;
    await adapter.save(1, draft, signal);
    expect(requests).toEqual([body, editedBody].map((text) => ({ name: webhook.name, enabled: true, type: 'webhook', config: { method: 'POST', content_type: 'JSON', query_params: query, body_template: text }, secrets: {} })));
  });
  test('Webhook JSON模式允许合法标量、数组及指数文本，非法文本仍返回字段错误', () => {
    const draft = newChannelDraft('webhook'); draft.name = 'fixture'; draft.secrets.url = { mode: 'replace', value: 'http://fixture.invalid/webhook' };
    for (const text of ['9007199254740993', '1e999', '1e-999', 'null', 'false', '"9007199254740993"', '[9007199254740993,{"n":1e30}]']) {
      draft.fields.body_template = text;
      const input = channelInput(draft);
      if (input.type !== 'webhook') throw new Error('测试渠道类型不符');
      expect(input.config?.body_template).toBe(text);
    }
    for (const [key, text] of [['body_template', '{broken'], ['query_params', '[9007199254740993]'], ['query_params', '{broken']] as const) {
      draft.fields.body_template = '{}'; draft.fields.query_params = '{}';
      draft.fields[key] = text;
      let failure: unknown;
      try { channelInput(draft); } catch (error) { failure = error; }
      expect(failure).toBeInstanceOf(ApiError);
      if (!(failure instanceof ApiError)) throw new Error('测试应返回字段错误');
      expect(failure.fields.map((field) => field.key)).toEqual([key]);
      expect(draft.fields[key]).toBe(text);
    }
  });
  test('Webhook旧对象响应不可自动转换成JSON文本', async () => {
    for (const key of ['query_params', 'body_template']) {
      const api = createApiClient({ fetch: async () => Response.json({ ...bark, type: 'webhook', config: { [key]: { external_id: 'fixture-id' } } }) });
      await expect(createChannelAdapter(api).get(1, new AbortController().signal)).rejects.toBeInstanceOf(ApiError);
    }
  });
  test('八个正式操作走生成client，测试无body且200失败保持失败，成员不截断', async () => {
    const calls: { method: string; path: string; body: unknown }[] = [];
    const api = createApiClient({ fetch: async (request) => {
      const path = new URL(request.url).pathname; const body = request.method === 'POST' && path.endsWith('/test') || request.method === 'GET' || request.method === 'DELETE' ? await request.text() : await request.json(); calls.push({ method: request.method, path, body });
      if (path.endsWith('/test')) return Response.json({ channel_id: 1, delivery_id: 4, at: 1700000000000, ok: false, error: { code: 'notify.error.http_status', args: { status: 401 } } });
      if (path.endsWith('/deliveries')) return Response.json([]);
      if (path.endsWith('/items')) return Response.json(Array.from({ length: 213 }, (_, i) => ({ finding_id: i + 1, task_id: null, title: null })));
      if (request.method === 'DELETE') return Response.json({ id: 1, affected_task_ids: [3] });
      if (request.method === 'GET' && path === '/api/v1/channels') return Response.json([bark, telegram]);
      return Response.json(path.endsWith('/2') ? telegram : bark, { status: request.method === 'POST' ? 201 : 200 });
    } });
    const adapter = createChannelAdapter(api); const signal = new AbortController().signal;
    expect((await adapter.list(signal)).map((row) => row.id)).toEqual([1, 2]); await adapter.get(2, signal);
    const draft = newChannelDraft('bark'); draft.name = 'fixture'; draft.secrets.url = { mode: 'replace', value: 'http://fixture.invalid/key' }; await adapter.save(null, draft, signal); await adapter.save(2, newChannelDraft('telegram', channelRow(telegram)), signal); await adapter.setEnabled(2, false, signal); await adapter.remove(1, signal);
    expect((await adapter.test(1, signal)).ok).toBe(false); await adapter.history(1, signal); expect(await adapter.deliveryItems(4, signal)).toHaveLength(213);
    expect(calls.map((call) => call.method + ' ' + call.path)).toEqual(['GET /api/v1/channels', 'GET /api/v1/channels/2', 'POST /api/v1/channels', 'PATCH /api/v1/channels/2', 'PATCH /api/v1/channels/2', 'DELETE /api/v1/channels/1', 'POST /api/v1/channels/1/test', 'GET /api/v1/channels/1/deliveries', 'GET /api/v1/deliveries/4/items']);
    expect(calls[3]?.body).toEqual({ name: telegram.name, enabled: true, type: 'telegram', config: telegram.config, secrets: {} }); expect(calls[4]?.body).toEqual({ enabled: false }); expect(calls[6]?.body).toBe('');
  });
});
