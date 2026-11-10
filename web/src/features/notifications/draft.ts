import { channelCatalog, type ChannelKind } from './catalog';
import type { ChannelDraft, ChannelRow, SecretEdit } from './types';

export function newChannelDraft(kind: ChannelKind = 'bark', channel?: ChannelRow): ChannelDraft {
  return {
    kind, name: channel?.name ?? '', enabled: channel?.enabled ?? true,
    fields: { ...(channel?.fields ?? {}), ...(kind === 'webhook' && !channel ? { method: 'POST', content_type: 'JSON' } : {}) },
    secrets: Object.fromEntries(channelCatalog[kind].fields.filter((field) => field.secret).map((field): [string, SecretEdit] => [field.key, { mode: channel?.hasSecret[field.key] ? 'keep' : 'replace', value: '' }])),
  };
}

export type DraftProblem = 'required' | 'json' | 'object' | 'port' | 'url' | 'send_key';
export function validateChannelDraft(draft: ChannelDraft): Record<string, DraftProblem> {
  const problems: Record<string, DraftProblem> = {};
  if (!draft.name.trim()) problems.name = 'required';
  for (const field of channelCatalog[draft.kind].fields) {
    const edit = draft.secrets[field.key];
    if (field.secret && edit?.mode === 'keep') continue;
    const text = field.secret ? (edit?.mode === 'clear' ? '' : edit?.value ?? '') : draft.fields[field.key] ?? '';
    if (field.required && !text.trim()) { problems[field.key] = 'required'; continue; }
    if (!text.trim()) continue;
    if (field.json) {
      try {
        const value: unknown = JSON.parse(text);
        const needsObject = field.key !== 'body_template' || draft.fields.method === 'POST' && draft.fields.content_type === 'FORM';
        if (needsObject && (value === null || typeof value !== 'object' || Array.isArray(value))) problems[field.key] = 'object';
      } catch { problems[field.key] = 'json'; }
    }
    if (field.numeric && (!/^\d+$/.test(text) || Number(text) < 1 || Number(text) > 65535)) problems[field.key] = 'port';
    if ((field.key === 'url' || field.key === 'api_url') && !validAddress(text)) problems[field.key] = 'url';
    if (field.key === 'send_key' && text.startsWith('sctp') && !/^sctp\d+t/.test(text)) problems[field.key] = 'send_key';
  }
  return problems;
}

function validAddress(value: string): boolean {
  try { const parsed = new URL(value); return ['http:', 'https:'].includes(parsed.protocol) && !!parsed.hostname && !parsed.hash && !/[\r\n]/.test(value); }
  catch { return false; }
}
