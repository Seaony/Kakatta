import type { RequestQuery, Schema } from '../api/types';

type Clearable<Value> = { [Key in keyof Value]?: Value[Key] | undefined };

export type ResultsSearch = Clearable<RequestQuery<'listFindings'>> & {
  finding_id?: Schema<'Finding'>['id'] | undefined;
  collection?: 'favorites' | 'dismissed' | undefined;
};
export type TasksSearch = Clearable<RequestQuery<'listTasks'>> & { task_id?: Schema<'Task'>['id'] | undefined; create_keyword?: string | undefined };
export type LogsSearch = Clearable<RequestQuery<'listRuns'>> & { run_id?: Schema<'RunView'>['id'] | undefined };
export type OverviewSearch = Clearable<RequestQuery<'getOverview'>>;
export type AccountsSearch = { account_id?: Schema<'AccountView'>['id'] | undefined };
export type AISearch = { provider_id?: number | undefined };
export type CompareSearch = { ids?: string | undefined };

export class InvalidSearchError extends Error {
  constructor() { super('页面筛选参数不正确'); this.name = 'InvalidSearchError'; }
}

function allowed(input: Record<string, unknown>, keys: readonly string[]) {
  if (Object.keys(input).some((key) => !keys.includes(key))) throw new InvalidSearchError();
}

function text(value: unknown, nonempty = false): string | undefined {
  if (value === undefined) return undefined;
  if (typeof value !== 'string' || nonempty && value.length === 0) throw new InvalidSearchError();
  return value;
}

function integer(value: unknown, minimum = Number.MIN_SAFE_INTEGER, maximum = Number.MAX_SAFE_INTEGER): number | undefined {
  if (value === undefined) return undefined;
  const parsed = typeof value === 'string' && /^-?\d+$/.test(value) ? Number(value) : value;
  if (typeof parsed !== 'number' || !Number.isSafeInteger(parsed) || parsed < minimum || parsed > maximum) throw new InvalidSearchError();
  return parsed;
}

function boolean(value: unknown): boolean | undefined {
  if (value === undefined) return undefined;
  if (value === true || value === 'true') return true;
  if (value === false || value === 'false') return false;
  throw new InvalidSearchError();
}

function choice<const Value extends string>(value: unknown, values: readonly Value[]): Value | undefined {
  if (value === undefined) return undefined;
  for (const candidate of values) if (candidate === value) return candidate;
  throw new InvalidSearchError();
}

export function resultsSearch(input: Record<string, unknown>): ResultsSearch {
  allowed(input, ['task_id', 'finding_id', 'collection', 'view', 'decision', 'price_drop', 'over_budget', 'q', 'sort', 'limit', 'cursor']);
  return {
    task_id: integer(input.task_id, 1), finding_id: integer(input.finding_id, 1),
    collection: choice(input.collection, ['favorites', 'dismissed']),
    view: choice(input.view, ['default', 'other', 'all', 'hidden']),
    decision: choice(input.decision, ['pending', 'scored', 'matched', 'rejected', 'failed', 'skipped']),
    price_drop: boolean(input.price_drop), over_budget: boolean(input.over_budget),
    q: text(input.q), sort: choice(input.sort, ['score', 'latest']),
    limit: integer(input.limit, 1, 200), cursor: text(input.cursor, true),
  };
}

export function resultsContextUnchanged(previous: ResultsSearch, next: ResultsSearch): boolean {
  const entries = (value: ResultsSearch) => Object.entries(value).filter(([key, field]) => key !== 'finding_id' && field !== undefined);
  const old = new Map(entries(previous)), current = new Map(entries(next));
  return old.size === current.size && [...old].every(([key, value]) => current.get(key) === value);
}

export function tasksSearch(input: Record<string, unknown>): TasksSearch {
  allowed(input, ['q', 'enabled', 'limit', 'cursor', 'task_id', 'create_keyword']);
  return { q: text(input.q), enabled: boolean(input.enabled), limit: integer(input.limit, 1, 200), cursor: text(input.cursor, true), task_id: integer(input.task_id, 1), create_keyword: text(input.create_keyword, true) };
}

export function logsSearch(input: Record<string, unknown>): LogsSearch {
  allowed(input, ['task_id', 'run_id', 'outcome', 'failure_kind', 'q', 'from_at', 'to_at', 'limit', 'cursor']);
  const from = integer(input.from_at), to = integer(input.to_at);
  if (from !== undefined && to !== undefined && from >= to) throw new InvalidSearchError();
  return {
    task_id: integer(input.task_id, 1), run_id: integer(input.run_id, 1),
    outcome: choice(input.outcome, ['success', 'failed', 'skipped']),
    failure_kind: choice(input.failure_kind, ['risk_verify', 'risk_deny', 'risk_env', 'login', 'network', 'page', 'timeout', 'quota', 'config', 'cancelled', 'restart']),
    q: text(input.q), from_at: from, to_at: to, limit: integer(input.limit, 1, 200), cursor: text(input.cursor, true),
  };
}

export function overviewSearch(input: Record<string, unknown>): OverviewSearch {
  allowed(input, ['period']);
  return { period: choice(input.period, ['today', '7d', '30d']) };
}

export function accountsSearch(input: Record<string, unknown>): AccountsSearch {
  allowed(input, ['account_id']);
  return { account_id: integer(input.account_id, 1) };
}

export function aiSearch(input: Record<string, unknown>): AISearch {
  allowed(input, ['provider_id']);
  return { provider_id: integer(input.provider_id, 1) };
}

export function comparisonIds(value: unknown): readonly number[] {
  if (value === undefined) return [];
  const values: unknown[] = Array.isArray(value) ? value : typeof value === 'string' ? value.split(',') : [value];
  if (values.length > 4 || values.length === 0) throw new InvalidSearchError();
  const result: number[] = [];
  for (const candidate of values) {
    const id = integer(candidate, 1);
    if (id === undefined || result.includes(id)) throw new InvalidSearchError();
    result.push(id);
  }
  return result;
}

export function compareSearch(input: Record<string, unknown>): CompareSearch {
  allowed(input, ['ids']);
  return { ids: input.ids === undefined ? undefined : comparisonIds(input.ids).join(',') };
}
