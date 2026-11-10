import type { components } from '../../api/schema.gen';

export const channelKinds = ['bark', 'telegram', 'ntfy', 'gotify', 'wecom', 'dingtalk', 'feishu', 'pushplus', 'serverchan', 'webhook', 'email'] as const;
export type ChannelKind = components['schemas']['NotifyType'];

const labels = {
  push_url: 'web.notify.field.push_url', topic_url: 'web.notify.field.topic_url',
  server_url: 'web.notify.field.server_url', webhook_url: 'web.notify.field.webhook_url', token: 'web.notify.field.token',
  chat_id: 'web.notify.field.chat_id', api_url: 'web.notify.field.api_url', secret: 'web.notify.field.secret',
  topic: 'web.notify.field.topic', send_key: 'web.notify.field.send_key', method: 'web.notify.field.method',
  headers: 'web.notify.field.headers', query_params: 'web.notify.field.query_params', content_type: 'web.notify.field.content_type',
  body_template: 'web.notify.field.body_template', host: 'web.notify.field.host', port: 'web.notify.field.port',
  encryption: 'web.notify.field.encryption', username: 'web.notify.field.username', password: 'web.notify.field.password',
  from: 'web.notify.field.from', to: 'web.notify.field.to',
} as const;

type Field = { key: string; label: typeof labels[keyof typeof labels]; required?: boolean; secret?: boolean; json?: boolean; numeric?: boolean; options?: readonly string[] };
const url = (label: 'push_url' | 'topic_url' | 'webhook_url'): Field => ({ key: 'url', label: labels[label], required: true, secret: true });
const token: Field = { key: 'token', label: labels.token, required: true, secret: true };
const secret: Field = { key: 'secret', label: labels.secret, secret: true };

export const channelCatalog: Record<ChannelKind, { label: `web.notify.kind.${ChannelKind}`; fields: readonly Field[]; help: `web.notify.help.${ChannelKind}` }> = {
  bark: { label: 'web.notify.kind.bark', fields: [url('push_url')], help: 'web.notify.help.bark' },
  telegram: { label: 'web.notify.kind.telegram', fields: [{ key: 'chat_id', label: labels.chat_id, required: true }, { key: 'api_url', label: labels.api_url }, token], help: 'web.notify.help.telegram' },
  ntfy: { label: 'web.notify.kind.ntfy', fields: [url('topic_url'), { ...token, required: false }], help: 'web.notify.help.ntfy' },
  gotify: { label: 'web.notify.kind.gotify', fields: [{ key: 'url', label: labels.server_url, required: true }, token], help: 'web.notify.help.gotify' },
  wecom: { label: 'web.notify.kind.wecom', fields: [url('webhook_url')], help: 'web.notify.help.wecom' },
  dingtalk: { label: 'web.notify.kind.dingtalk', fields: [url('webhook_url'), secret], help: 'web.notify.help.dingtalk' },
  feishu: { label: 'web.notify.kind.feishu', fields: [url('webhook_url'), secret], help: 'web.notify.help.feishu' },
  pushplus: { label: 'web.notify.kind.pushplus', fields: [token, { key: 'topic', label: labels.topic }], help: 'web.notify.help.pushplus' },
  serverchan: { label: 'web.notify.kind.serverchan', fields: [{ key: 'send_key', label: labels.send_key, required: true, secret: true }], help: 'web.notify.help.serverchan' },
  webhook: { label: 'web.notify.kind.webhook', fields: [url('webhook_url'), { key: 'method', label: labels.method, options: ['POST', 'GET'] }, { key: 'headers', label: labels.headers, secret: true, json: true }, { key: 'query_params', label: labels.query_params, json: true }, { key: 'content_type', label: labels.content_type, options: ['JSON', 'FORM'] }, { key: 'body_template', label: labels.body_template, json: true }], help: 'web.notify.help.webhook' },
  email: { label: 'web.notify.kind.email', fields: [{ key: 'host', label: labels.host, required: true }, { key: 'port', label: labels.port, required: true, numeric: true }, { key: 'encryption', label: labels.encryption, required: true, options: ['ssl', 'starttls', 'none'] }, { key: 'username', label: labels.username }, { key: 'password', label: labels.password, secret: true }, { key: 'from', label: labels.from, required: true }, { key: 'to', label: labels.to, required: true }], help: 'web.notify.help.email' },
};

export const webhookVariables = ['title', 'content', 'body', 'price', 'reason', 'score', 'grade', 'config', 'fair_price', 'desktop_link', 'mobile_link', 'link', 'kakatta_url', 'urgent', 'item_id', 'item_title', 'image', 'task', 'kind'] as const;
export type ChannelField = Field;
