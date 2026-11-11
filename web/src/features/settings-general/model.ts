import type { components } from '../../api/schema.gen';

export type Definition = components['schemas']['SettingDefinition'];
export type Snapshot = components['schemas']['SettingsSnapshot'];
export type JsonValue = components['schemas']['JsonValue'];
export type Message = components['schemas']['LocalizedMessage'];
export type FieldIssue = components['schemas']['FieldIssue'];
export type SettingOwner = 'general' | 'limits' | 'ai' | 'notifications' | 'security' | 'about' | 'onboarding';

export type Draft =
  | { kind: 'scalar'; text: string }
  | { kind: 'boolean'; checked: boolean }
  | { kind: 'range'; min: string; max: string }
  | { kind: 'quiet_hours'; enabled: boolean; start: string; end: string };
export type Drafts = Record<string, Draft>;

type Parsed = { ok: true; value: JsonValue } | { ok: false; reason: Message };

export const generalGroups = [
  ['general.language', 'general.appearance'],
  ['general.timezone', 'crawl.quiet_hours'],
  ['general.public_url'],
  ['retention.findings_days', 'retention.run_steps_days', 'retention.stats_days', 'retention.seller_profile_days', 'retention.orphan_listing_days'],
  ['cache.image_max_mb', 'cache.browser_cache_mb', 'cleanup.interval_hours'],
] as const;

export const limitGroups = [
  ['crawl.task_interval_minutes', 'crawl.interval_jitter_percent', 'crawl.account_min_gap_seconds', 'crawl.account_min_gap_jitter_seconds', 'crawl.account_hourly_visits', 'crawl.account_daily_search_visits', 'crawl.account_daily_detail_visits'],
  ['crawl.max_pages', 'crawl.page_wait_seconds', 'crawl.max_details_per_run', 'crawl.detail_pre_wait_seconds', 'crawl.detail_stay_seconds', 'crawl.action_wait_seconds', 'crawl.home_stay_seconds', 'crawl.typing_interval_ms', 'crawl.seller_cache_hours', 'crawl.global_pages'],
  ['crawl.verify_hold_minutes', 'crawl.verify_cooldown_base_minutes', 'crawl.verify_cooldown_max_minutes', 'crawl.verify_cooldown_reset_hours', 'crawl.deny_pause_hours', 'crawl.failure_threshold', 'crawl.failure_pause_minutes', 'crawl.run_timeout_minutes'],
] as const;

export function settingOwner(key: string): SettingOwner | null {
  if (key === 'crawl.quiet_hours') return 'general';
  if (key.startsWith('crawl.')) return 'limits';
  if (key.startsWith('ai.')) return 'ai';
  if (key.startsWith('notify.')) return 'notifications';
  if (key.startsWith('security.')) return 'security';
  if (key === 'general.update_check') return 'about';
  if (key === 'general.onboarding_done') return 'onboarding';
  if (['general.', 'retention.', 'cache.', 'cleanup.'].some((prefix) => key.startsWith(prefix))) return 'general';
  return null;
}

function object(value: JsonValue | undefined): value is Record<string, JsonValue> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

export function makeDraft(definition: Definition, value: JsonValue | undefined): Draft | null {
  if (definition.kind === 'range') {
    if (!object(value) || typeof value.min !== 'number' || typeof value.max !== 'number') return null;
    return { kind: 'range', min: String(value.min), max: String(value.max) };
  }
  if (definition.kind === 'quiet_hours') {
    if (!object(value) || typeof value.enabled !== 'boolean' || typeof value.start !== 'string' || typeof value.end !== 'string') return null;
    return { kind: 'quiet_hours', enabled: value.enabled, start: value.start, end: value.end };
  }
  if (definition.kind === 'boolean' && typeof value === 'boolean') return { kind: 'boolean', checked: value };
  if ((definition.kind === 'integer' && typeof value === 'number') || (definition.kind === 'string' && typeof value === 'string')) {
    return { kind: 'scalar', text: String(value) };
  }
  return null;
}

const error = (code: string): Parsed => ({ ok: false, reason: { code, args: {} } });

function number(text: string, integer: boolean): Parsed {
  if (!text.trim() || (integer && !/^-?\d+$/.test(text.trim()))) return error(integer ? 'settings.integer_required' : 'settings.range_required');
  const value = Number(text);
  if (!Number.isFinite(value)) return error('settings.overflow');
  // 草稿始终保留原文，不能在转成请求数字时悄悄舍入。
  if (integer && !Number.isSafeInteger(value)) return error('web.settings.number_precision');
  return { ok: true, value };
}

export function parseDraft(definition: Definition, draft: Draft): Parsed {
  if (draft.kind === 'boolean') return { ok: true, value: draft.checked };
  if (draft.kind === 'quiet_hours') {
    if (![draft.start, draft.end].every((clock) => /^(?:[01]\d|2[0-3]):[0-5]\d$/.test(clock))) return error('settings.invalid_clock');
    return { ok: true, value: { enabled: draft.enabled, start: draft.start, end: draft.end } };
  }
  if (draft.kind === 'range') {
    const min = number(draft.min, false);
    const max = number(draft.max, false);
    if (!min.ok) return min;
    if (!max.ok) return max;
    if (typeof min.value !== 'number' || typeof max.value !== 'number' || min.value <= 0 || max.value < min.value) return error('settings.invalid_range');
    return { ok: true, value: { min: min.value, max: max.value } };
  }
  if (definition.kind === 'integer') return number(draft.text, true);
  return { ok: true, value: draft.text };
}

export function sameDraft(a: Draft, b: Draft): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

export function draftWarnings(definition: Definition, draft: Draft): Message[] {
  if (draft.kind === 'quiet_hours') return !draft.enabled || draft.start === draft.end ? [{ code: 'settings.quiet_disabled', args: {} }] : [];
  const parsed = parseDraft(definition, draft);
  if (!parsed.ok) return [];
  const value = parsed.value;
  let min: number | undefined;
  let max: number | undefined;
  if (typeof value === 'number') min = max = value;
  if (object(value) && typeof value.min === 'number' && typeof value.max === 'number') { min = value.min; max = value.max; }
  const recommendation = definition.recommendation;
  const warnings: Message[] = [];
  if (min !== undefined && max !== undefined && ((recommendation.min !== null && min < recommendation.min) || (recommendation.max !== null && max > recommendation.max))) {
    warnings.push({ code: 'settings.outside_recommendation', args: {} });
  }
  if (definition.key === 'retention.stats_days' && typeof value === 'number' && value < 31) warnings.push({ code: 'settings.stats_incomplete', args: {} });
  return warnings;
}

export function fieldWarnings(definition: Definition, draft: Draft, saved: readonly Message[]): Message[] {
  return [...saved, ...draftWarnings(definition, draft)].filter((message, index, all) => all.findIndex((item) => item.code === message.code) === index);
}

export function buildPatch(definitions: readonly Definition[], drafts: Drafts, snapshot: Snapshot): { values: components['schemas']['SettingValues']; fields: FieldIssue[] } {
  const values: components['schemas']['SettingValues'] = {};
  const fields: FieldIssue[] = [];
  for (const definition of definitions) {
    const draft = drafts[definition.key];
    if (!draft) continue;
    if (snapshot.sources[definition.key] === 'environment') continue;
    const baseline = makeDraft(definition, snapshot.values[definition.key]);
    if (!baseline || sameDraft(draft, baseline)) continue;
    const parsed = parseDraft(definition, draft);
    if (parsed.ok) values[definition.key] = parsed.value;
    else fields.push({ key: definition.key, reason: parsed.reason });
  }
  return { values, fields };
}
