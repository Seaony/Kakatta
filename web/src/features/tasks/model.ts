import type { Schema } from '../../api/types';

type TaskFields = Schema<'TaskFields'>;
type Profile = Schema<'AnalysisProfileFields'>;

export type ProfileRow<T> = { key: string; value: T };
export type ProfileDraft = Omit<Profile, 'specs' | 'musts' | 'bonuses' | 'risks'> & {
  specs: ProfileRow<Schema<'AnalysisProfileSpec'>>[];
  musts: ProfileRow<Schema<'AnalysisRequirement'>>[];
  bonuses: ProfileRow<Schema<'AnalysisRequirement'>>[];
  risks: ProfileRow<Schema<'AnalysisProfileRisk'>>[];
};
export type TaskDraft = Omit<TaskFields, 'profile' | 'min_price' | 'max_price' | 'budget_min' | 'budget_max' | 'interval_minutes' | 'max_pages' | 'match_keywords' | 'exclude_keywords'> & {
  profile: ProfileDraft;
  min_price: string;
  max_price: string;
  budget_min: string;
  budget_max: string;
  interval_minutes: string;
  max_pages: string;
  match_keywords_text: string;
  exclude_keywords_text: string;
};

export type ValidationReason = 'required' | 'name_length' | 'keyword_length' | 'money' | 'range' | 'invalid_range' | 'positive_integer' | 'profile' | 'account' | 'option';
export type DraftIssue = { field: string; reason: ValidationReason };
export type PreparedTask = { body: Schema<'TaskCreate'>; issues: [] } | { body: null; issues: DraftIssue[] };

let rowSequence = 0;
export function profileRow<T>(value: T): ProfileRow<T> {
  rowSequence += 1;
  return { key: `draft-${rowSequence}`, value };
}

function isProfile(value: Schema<'AnalysisProfile'>): value is Profile {
  return 'specs' in value && Array.isArray(value.specs);
}

export function emptyProfile(): ProfileDraft {
  return { summary: '', specs: [], musts: [], bonuses: [], risks: [], allowed_kinds: [], next_no: 1 };
}

export function profileDraft(value: Schema<'AnalysisProfile'>): ProfileDraft {
  if (!isProfile(value)) return emptyProfile();
  return {
    summary: value.summary,
    next_no: value.next_no,
    allowed_kinds: [...value.allowed_kinds],
    specs: value.specs.map((spec) => profileRow({ ...spec, options: [...spec.options], values: [...spec.values] })),
    musts: value.musts.map((item) => profileRow({ ...item })),
    bonuses: value.bonuses.map((item) => profileRow({ ...item })),
    risks: value.risks.map((item) => profileRow({ ...item })),
  };
}

export function profileInput(value: ProfileDraft): Schema<'AnalysisProfileInput'> {
  return {
    summary: value.summary,
    next_no: value.next_no,
    allowed_kinds: value.allowed_kinds,
    specs: value.specs.map((row) => row.value),
    musts: value.musts.map((row) => row.value),
    bonuses: value.bonuses.map((row) => row.value),
    risks: value.risks.map((row) => row.value),
  };
}

const generalChecks = new Set(['photo_mismatch', 'stock_photo', 'vague', 'off_platform', 'deposit', 'pickup_only', 'no_return', 'no_inspection', 'seller_dealer', 'seller_new', 'seller_bad_reviews', 'seller_partial_refund']);
export function initialProfile(dictionary: Schema<'AnalysisRiskDefinition'>[]): ProfileDraft {
  const profile = emptyProfile();
  profile.risks = dictionary.filter((check) => generalChecks.has(check.id)).map((check) => profileRow({ id: check.id, attitude: check.default_attitude, source: 'system', edited: false, rev: 1 }));
  return profile;
}

export function moneyInput(cents: number | null): string {
  if (cents === null) return '';
  const whole = Math.trunc(cents / 100);
  const remainder = Math.abs(cents % 100);
  return remainder === 0 ? String(whole) : `${whole}.${String(remainder).padStart(2, '0')}`;
}

// 输入时保留原文，提交时才精确换算，避免输入小数时跳光标。
export function parseMoney(value: string): number | null | undefined {
  const text = value.trim();
  if (text === '') return null;
  const match = /^(\d+)(?:\.(\d{1,2}))?$/.exec(text);
  if (!match?.[1]) return undefined;
  const cents = BigInt(match[1]) * 100n + BigInt((match[2] ?? '').padEnd(2, '0'));
  if (cents > BigInt(Number.MAX_SAFE_INTEGER)) return undefined;
  return Number(cents);
}

export function suggestedSearchPrice(budgetMax: number | null | undefined): number | null {
  if (typeof budgetMax !== 'number' || !Number.isSafeInteger(budgetMax) || budgetMax < 0) return null;
  // 先放宽两成，再按十元四舍五入，全程用整数分计算。
  const cents = ((BigInt(budgetMax) * 6n + 2500n) / 5000n) * 1000n;
  return cents <= BigInt(Number.MAX_SAFE_INTEGER) ? Number(cents) : null;
}

export function splitWords(text: string): string[] {
  const seen = new Set<string>();
  return text.split(/[\n,，]/u).map((word) => word.trim()).filter((word) => {
    const key = word.normalize('NFKC').toLocaleLowerCase('en').replace(/\s+/gu, ' ');
    if (!key || seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

export function draftFromFields(fields: TaskFields, initialKeyword?: string): TaskDraft {
  return {
    name: fields.name,
    keyword: initialKeyword ?? fields.keyword,
    enabled: fields.enabled,
    account_id: fields.account_id,
    account_pinned: fields.account_pinned,
    personal_only: fields.personal_only,
    free_shipping_only: fields.free_shipping_only,
    publish_window: fields.publish_window,
    region_province: fields.region_province,
    region_city: fields.region_city,
    region_district: fields.region_district,
    crawl_overrides: structuredClone(fields.crawl_overrides),
    judge_mode: fields.judge_mode,
    requirement: fields.requirement,
    profile: profileDraft(fields.profile),
    min_price: moneyInput(fields.min_price),
    max_price: moneyInput(fields.max_price),
    budget_min: moneyInput(fields.budget_min),
    budget_max: moneyInput(fields.budget_max),
    interval_minutes: String(fields.interval_minutes),
    max_pages: String(fields.max_pages),
    match_keywords_text: fields.match_keywords.join('\n'),
    exclude_keywords_text: fields.exclude_keywords.join('\n'),
    analyze_on_first_run: fields.analyze_on_first_run,
    notify_policy: fields.notify_policy,
    notify_price_drop: fields.notify_price_drop,
    channel_ids: [...fields.channel_ids],
  };
}

export function publishWindow(value: string): Schema<'TaskCreate'>['publish_window'] {
  switch (value) {
    case 'any': case 'day1': case 'day3': case 'day7': case 'day14': return value;
    default: return undefined;
  }
}

export function notifyPolicy(value: string): Schema<'TaskCreate'>['notify_policy'] {
  switch (value) {
    case 'steal': case 'good': case 'fair': case 'all_new': case 'none': case 'recommended': return value;
    default: return undefined;
  }
}

export const taskOverrideKeys = ['interval_jitter_percent', 'page_wait_seconds', 'max_details_per_run', 'detail_pre_wait_seconds', 'detail_stay_seconds', 'action_wait_seconds', 'home_stay_seconds', 'typing_interval_ms', 'run_timeout_minutes'] as const;
const rangeOverrideKeys = new Set(['page_wait_seconds', 'detail_pre_wait_seconds', 'detail_stay_seconds', 'action_wait_seconds', 'home_stay_seconds', 'typing_interval_ms']);

function overrideNumber(value: Schema<'JsonValue'> | undefined): number | undefined {
  if (typeof value !== 'number' && typeof value !== 'string') return undefined;
  if (typeof value === 'string' && !/^\d+$/u.test(value.trim())) return undefined;
  const number = Number(value);
  return Number.isSafeInteger(number) ? number : undefined;
}

function overrideRangeNumber(value: Schema<'JsonValue'> | undefined): number | undefined {
  if (typeof value !== 'number' && typeof value !== 'string') return undefined;
  if (typeof value === 'string' && !/^(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?$/iu.test(value.trim())) return undefined;
  const number = Number(value);
  return Number.isFinite(number) && number > 0 ? number : undefined;
}

export function prepareOverrides(values: Schema<'SettingValues'>): { values: Schema<'SettingValues'>; issues: DraftIssue[] } {
  const result: Schema<'SettingValues'> = {};
  const issues: DraftIssue[] = [];
  for (const [key, value] of Object.entries(values)) {
    const field = `crawl_overrides.${key}`;
    if (!taskOverrideKeys.some((allowed) => allowed === key)) { issues.push({ field, reason: 'option' }); continue; }
    if (rangeOverrideKeys.has(key)) {
      if (value === null || typeof value !== 'object' || Array.isArray(value)) { issues.push({ field, reason: 'invalid_range' }); continue; }
      const min = overrideRangeNumber(value.min), max = overrideRangeNumber(value.max);
      if (min === undefined || max === undefined) issues.push({ field, reason: 'invalid_range' });
      else if (min > max) issues.push({ field, reason: 'range' });
      else result[key] = { min, max };
    } else {
      const number = overrideNumber(value);
      if (number === undefined || number < (key === 'interval_jitter_percent' ? 0 : 1)) issues.push({ field, reason: 'positive_integer' });
      else result[key] = number;
    }
  }
  return { values: result, issues };
}

export function prepareTask(draft: TaskDraft): PreparedTask {
  const issues: DraftIssue[] = [];
  const keyword = draft.keyword.trim();
  const name = draft.name.trim();
  if (!keyword) issues.push({ field: 'keyword', reason: 'required' });
  else if ([...keyword].length > 50) issues.push({ field: 'keyword', reason: 'keyword_length' });
  if ([...name].length > 30) issues.push({ field: 'name', reason: 'name_length' });
  const minPrice = parseMoney(draft.min_price), maxPrice = parseMoney(draft.max_price);
  const budgetMin = parseMoney(draft.budget_min), budgetMax = parseMoney(draft.budget_max);
  for (const [field, value] of [['min_price', minPrice], ['max_price', maxPrice], ['budget_min', budgetMin], ['budget_max', budgetMax]] as const) {
    if (value === undefined) issues.push({ field, reason: 'money' });
  }
  if (minPrice != null && maxPrice != null && minPrice > maxPrice) issues.push({ field: 'max_price', reason: 'range' });
  if (budgetMin != null && budgetMax != null && budgetMin > budgetMax) issues.push({ field: 'budget_max', reason: 'range' });
  const interval = Number(draft.interval_minutes), pages = Number(draft.max_pages);
  if (!/^\d+$/u.test(draft.interval_minutes.trim()) || !Number.isSafeInteger(interval) || interval < 1) issues.push({ field: 'interval_minutes', reason: 'positive_integer' });
  if (!/^\d+$/u.test(draft.max_pages.trim()) || !Number.isSafeInteger(pages) || pages < 1) issues.push({ field: 'max_pages', reason: 'positive_integer' });
  if (draft.account_pinned && (!draft.account_id || !Number.isSafeInteger(draft.account_id))) issues.push({ field: 'account_id', reason: 'account' });
  const window = publishWindow(draft.publish_window), policy = notifyPolicy(draft.notify_policy);
  if (!window) issues.push({ field: 'publish_window', reason: 'option' });
  if (!policy || (draft.judge_mode === 'keyword' && !['recommended', 'all_new', 'none'].includes(policy)) || (draft.judge_mode === 'ai' && policy === 'recommended')) issues.push({ field: 'notify_policy', reason: 'option' });
  if (draft.judge_mode === 'ai' && draft.profile.specs.length === 0 && draft.profile.musts.length === 0) issues.push({ field: 'profile', reason: 'profile' });
  const overrides = prepareOverrides(draft.crawl_overrides);
  issues.push(...overrides.issues);
  if (issues.length || minPrice === undefined || maxPrice === undefined || budgetMin === undefined || budgetMax === undefined || !window || !policy) return { body: null, issues };
  return { issues: [], body: {
    keyword,
    ...(name ? { name } : {}),
    enabled: draft.enabled,
    account_id: draft.account_id,
    account_pinned: draft.account_pinned,
    min_price: minPrice, max_price: maxPrice,
    personal_only: draft.personal_only, free_shipping_only: draft.free_shipping_only,
    publish_window: window,
    region_province: draft.region_province, region_city: draft.region_city, region_district: draft.region_district,
    max_pages: pages, interval_minutes: interval, crawl_overrides: overrides.values,
    judge_mode: draft.judge_mode, requirement: draft.requirement,
    profile: draft.judge_mode === 'ai' ? profileInput(draft.profile) : {},
    budget_min: budgetMin, budget_max: budgetMax,
    match_keywords: splitWords(draft.match_keywords_text), exclude_keywords: splitWords(draft.exclude_keywords_text),
    analyze_on_first_run: draft.analyze_on_first_run,
    notify_policy: policy, notify_price_drop: draft.notify_price_drop,
    channel_ids: draft.channel_ids,
  } };
}

export function searchChanged(initial: TaskFields, draft: TaskDraft): boolean {
  return initial.keyword !== draft.keyword.trim() || initial.min_price !== parseMoney(draft.min_price) || initial.max_price !== parseMoney(draft.max_price)
    || initial.personal_only !== draft.personal_only || initial.free_shipping_only !== draft.free_shipping_only
    || initial.publish_window !== draft.publish_window || initial.region_province !== draft.region_province
    || initial.region_city !== draft.region_city || initial.region_district !== draft.region_district;
}

function sameValue(left: unknown, right: unknown): boolean {
  if (Object.is(left, right)) return true;
  if (Array.isArray(left) && Array.isArray(right)) return left.length === right.length && left.every((value, index) => sameValue(value, right[index]));
  if (left === null || right === null || typeof left !== 'object' || typeof right !== 'object') return false;
  const keys = Object.keys(left);
  return keys.length === Object.keys(right).length && keys.every((key) => Object.hasOwn(right, key) && sameValue(Reflect.get(left, key), Reflect.get(right, key)));
}

const writableTaskKeys = ['name', 'keyword', 'enabled', 'account_id', 'account_pinned', 'min_price', 'max_price', 'personal_only', 'free_shipping_only', 'publish_window', 'region_province', 'region_city', 'region_district', 'max_pages', 'interval_minutes', 'crawl_overrides', 'judge_mode', 'requirement', 'profile', 'budget_min', 'budget_max', 'match_keywords', 'exclude_keywords', 'analyze_on_first_run', 'notify_policy', 'notify_price_drop', 'channel_ids'] as const satisfies readonly (keyof Schema<'TaskPatch'>)[];

// 普通编辑只提交改过的字段，避免把旧的账号、渠道或清单写回去。
export function changedTaskPatch(base: TaskFields, body: Schema<'TaskCreate'>): Schema<'TaskPatch'> {
  const patch: Schema<'TaskPatch'> = {};
  const take = <K extends keyof Schema<'TaskPatch'>>(key: K, value: Schema<'TaskPatch'>[K]) => {
    if (value !== undefined && !sameValue(base[key], value)) patch[key] = value;
  };
  for (const key of writableTaskKeys) take(key, body[key]);
  if (Object.hasOwn(patch, 'account_id') || Object.hasOwn(patch, 'account_pinned')) {
    patch.account_id = body.account_id ?? null;
    patch.account_pinned = body.account_pinned ?? false;
  }
  if (Object.hasOwn(patch, 'profile') && body.judge_mode !== undefined) patch.judge_mode = body.judge_mode;
  return patch;
}
