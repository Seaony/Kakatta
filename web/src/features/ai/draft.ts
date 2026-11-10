import type { Schema } from '../../api/types';

export const aiSettingKeys = [
  'ai.max_items_per_run',
  'ai.image_count',
  'ai.daily_token_limit',
  'ai.proxy_id',
  'ai.max_concurrent_requests',
  'ai.request_timeout_seconds',
] as const;

export type AISettingKey = (typeof aiSettingKeys)[number];
export type AISettingsDraft = Partial<Record<AISettingKey, string>>;

export function parseSettingInteger(key: AISettingKey, value: string): number | null {
  if (key === 'ai.proxy_id' && value === '') return null;
  if (!/^\d+$/.test(value)) throw new RangeError('integer');
  const number = Number(value);
  if (!Number.isSafeInteger(number) || number < 0 || (number === 0 && key !== 'ai.daily_token_limit')) {
    throw new RangeError('integer');
  }
  return number;
}

export type ProviderDraft = {
  name: string;
  preset: string;
  baseURL: string;
  visionModel: string;
  textModel: string;
  enabled: boolean;
  keyAction: 'keep' | 'replace' | 'remove';
  keyValue: string;
};

export function emptyProviderDraft(): ProviderDraft {
  return { name: '', preset: 'custom', baseURL: '', visionModel: '', textModel: '', enabled: true, keyAction: 'keep', keyValue: '' };
}

export const promptKinds = ['analysis', 'title_screen', 'profile', 'market'] as const;
export type PromptKind = Schema<'ModelClientKind'>;

export function missingPromptKeys(content: string, keys: readonly string[]): string[] {
  return keys.filter((key) => !content.includes(`"${key}"`));
}
