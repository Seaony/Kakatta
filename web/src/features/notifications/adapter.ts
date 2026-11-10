import type { ApiClient } from '../../api/client';
import { apiData } from '../../api/hooks';
import { ApiError } from '../../api/errors';
import type { components } from '../../api/schema.gen';
import { validateChannelDraft } from './draft';
import type { ChannelAdapter, ChannelDraft, ChannelRow } from './types';

type Schema<Name extends keyof components['schemas']> = components['schemas'][Name];
function invalid(key: string): never { throw new ApiError(422, { code: 'settings.invalid', args: {} }, [{ key, reason: { code: 'notify.error.invalid_config', args: {} } }]); }
function isJson(value: unknown): value is Schema<'JsonValue'> {
  if (value === null || typeof value === 'string' || typeof value === 'boolean') return true;
  if (typeof value === 'number') return Number.isFinite(value);
  if (Array.isArray(value)) return value.every(isJson);
  return typeof value === 'object' && Object.values(value).every(isJson);
}
function json(text: string, key: string): Schema<'JsonValue'> { try { const value: unknown = JSON.parse(text); if (isJson(value)) return value; } catch { /* 下面只返回固定字段原因。 */ } return invalid(key); }
function object(text: string, key: string): Record<string, Schema<'JsonValue'>> { const value = json(text, key); if (value !== null && typeof value === 'object' && !Array.isArray(value)) return value; return invalid(key); }
function secret(draft: ChannelDraft, key: string): string | undefined { const edit = draft.secrets[key]; return !edit || edit.mode === 'keep' ? undefined : edit.mode === 'clear' ? '' : edit.value; }

export function channelInput(draft: ChannelDraft): Schema<'ChannelCreate'> {
  const problems = validateChannelDraft(draft); const problem = Object.keys(problems)[0]; if (problem) return invalid(problem);
  const base = { name: draft.name, enabled: draft.enabled };
  const url = secret(draft, 'url'), token = secret(draft, 'token'), signing = secret(draft, 'secret'), password = secret(draft, 'password'), sendKey = secret(draft, 'send_key');
  const urls = url === undefined ? {} : { url }; const tokens = token === undefined ? {} : { token }; const signatures = signing === undefined ? {} : { secret: signing };
  switch (draft.kind) {
    case 'bark': return { ...base, type: 'bark', config: {}, secrets: urls };
    case 'telegram': return { ...base, type: 'telegram', config: { chat_id: draft.fields.chat_id ?? '', api_url: draft.fields.api_url ?? '' }, secrets: tokens };
    case 'ntfy': return { ...base, type: 'ntfy', config: {}, secrets: { ...urls, ...tokens } };
    case 'gotify': return { ...base, type: 'gotify', config: { url: draft.fields.url ?? '' }, secrets: tokens };
    case 'wecom': return { ...base, type: 'wecom', config: {}, secrets: urls };
    case 'dingtalk': return { ...base, type: 'dingtalk', config: {}, secrets: { ...urls, ...signatures } };
    case 'feishu': return { ...base, type: 'feishu', config: {}, secrets: { ...urls, ...signatures } };
    case 'pushplus': return { ...base, type: 'pushplus', config: { topic: draft.fields.topic ?? '' }, secrets: tokens };
    case 'serverchan': return { ...base, type: 'serverchan', config: {}, secrets: sendKey === undefined ? {} : { send_key: sendKey } };
    case 'webhook': {
      const method = draft.fields.method || 'POST', content = draft.fields.content_type || 'JSON';
      if (method !== 'GET' && method !== 'POST') return invalid('method');
      if (content !== 'JSON' && content !== 'FORM') return invalid('content_type');
      const config: Schema<'ChannelWebhookConfig'> = { method, content_type: content };
      if (draft.fields.query_params?.trim()) config.query_params = draft.fields.query_params;
      if (draft.fields.body_template?.trim()) config.body_template = draft.fields.body_template;
      const secrets: Schema<'ChannelWebhookSecrets'> = { ...urls };
      const edit = draft.secrets.headers;
      if (edit && edit.mode !== 'keep') {
        const headers: Record<string, string> = {};
        if (edit.mode !== 'clear' && edit.value.trim()) for (const [key, value] of Object.entries(object(edit.value, 'headers'))) { if (typeof value !== 'string') return invalid('headers'); headers[key] = value; }
        secrets.headers = headers;
      }
      return { ...base, type: 'webhook', config, secrets };
    }
    case 'email': {
      const encryption = draft.fields.encryption;
      if (encryption !== 'ssl' && encryption !== 'starttls' && encryption !== 'none') return invalid('encryption');
      return { ...base, type: 'email', config: { host: draft.fields.host ?? '', port: Number(draft.fields.port), encryption, username: draft.fields.username ?? '', from: draft.fields.from ?? '', to: draft.fields.to ?? '' }, secrets: password === undefined ? {} : { password } };
    }
  }
}

export function channelRow(channel: Schema<'Channel'>): ChannelRow {
  const fields: Record<string, string> = {};
  if (channel.type === 'webhook') {
    fields.method = channel.config.method || 'POST'; fields.content_type = channel.config.content_type || 'JSON';
    for (const key of ['query_params', 'body_template'] as const) {
      const text = channel.config[key];
      if (text === undefined) continue;
      if (typeof text !== 'string') throw new ApiError(0, { code: 'web.invalid_response', args: {} });
      fields[key] = text;
    }
  } else for (const [key, value] of Object.entries<unknown>(channel.config)) if (value !== undefined) fields[key] = typeof value === 'string' ? value : JSON.stringify(value);
  return { id: channel.id, kind: channel.type, name: channel.name, enabled: channel.enabled, fields, hasSecret: channel.secret_set, testedAt: channel.last_test_at, testOK: channel.last_test_ok, testError: channel.last_error };
}

export function createChannelAdapter(api: ApiClient): ChannelAdapter {
  return {
    list: async (signal) => apiData(await api.GET('/api/v1/channels', { signal })).map(channelRow),
    get: async (id, signal) => channelRow(apiData(await api.GET('/api/v1/channels/{channel_id}', { params: { path: { channel_id: id } }, signal }))),
    save: async (id, draft, signal) => {
      const body = channelInput(draft);
      return channelRow(id === null
        ? apiData(await api.POST('/api/v1/channels', { body, signal }))
        : apiData(await api.PATCH('/api/v1/channels/{channel_id}', { params: { path: { channel_id: id } }, body, signal })));
    },
    setEnabled: async (id, enabled, signal) => channelRow(apiData(await api.PATCH('/api/v1/channels/{channel_id}', { params: { path: { channel_id: id } }, body: { enabled }, signal }))),
    remove: async (id, signal) => { apiData(await api.DELETE('/api/v1/channels/{channel_id}', { params: { path: { channel_id: id } }, signal })); },
    test: async (id, signal) => apiData(await api.POST('/api/v1/channels/{channel_id}/test', { params: { path: { channel_id: id } }, signal })),
    history: async (id, signal) => apiData(await api.GET('/api/v1/channels/{channel_id}/deliveries', { params: { path: { channel_id: id } }, signal })),
    deliveryItems: async (id, signal) => apiData(await api.GET('/api/v1/deliveries/{delivery_id}/items', { params: { path: { delivery_id: id } }, signal })),
  };
}
