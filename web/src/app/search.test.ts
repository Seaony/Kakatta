import { expect, test } from 'bun:test';
import description from '../../../api/openapi.json';
import { accountsSearch, aiSearch, compareSearch, comparisonIds, InvalidSearchError, logsSearch, overviewSearch, resultsContextUnchanged, resultsSearch, tasksSearch } from './search';

test('深链保留结果上下文与预填词，清除字段可显式传undefined', () => {
  const result = resultsSearch({ task_id: '7', finding_id: 23, view: 'other', q: '256 或 512', price_drop: 'false', sort: 'latest', collection: 'favorites' });
  expect(result.task_id).toBe(7);
  expect(result.finding_id).toBe(23);
  expect(result.price_drop).toBe(false);
  expect(result.q).toBe('256 或 512');
  expect(resultsSearch({ ...result, finding_id: undefined }).finding_id).toBeUndefined();
  expect(tasksSearch({ create_keyword: 'iPad mini', q: '旧任务' })).toMatchObject({ create_keyword: 'iPad mini', q: '旧任务' });
});

test('非法编号、重复参数、陌生筛选和损坏游标不悄悄变成第一页', () => {
  for (const input of [{ task_id: '9007199254740993' }, { finding_id: -1 }, { q: ['one', 'two'] }, { view: 'unknown' }, { cursor: '' }, { random: true }]) {
    expect(() => resultsSearch(input)).toThrow(InvalidSearchError);
  }
  expect(() => accountsSearch({ account_id: 0 })).toThrow(InvalidSearchError);
  expect(() => aiSearch({ provider_id: 1.5 })).toThrow(InvalidSearchError);
});

test('日志与结果枚举和真实OpenAPI一致，时间范围含起不含终且不倒置', () => {
  for (const failure_kind of description.components.schemas.FailureKind.enum) expect(logsSearch({ failure_kind }).failure_kind === failure_kind).toBe(true);
  for (const outcome of description.components.schemas.RunOutcome.enum) expect(logsSearch({ outcome }).outcome === outcome).toBe(true);
  for (const decision of description.components.schemas.AnalysisDecision.enum) expect(resultsSearch({ decision }).decision === decision).toBe(true);
  expect(logsSearch({ from_at: '0', to_at: '42', run_id: '5' })).toMatchObject({ from_at: 0, to_at: 42, run_id: 5 });
  expect(() => logsSearch({ from_at: 42, to_at: 42 })).toThrow(InvalidSearchError);
  expect(overviewSearch({ period: '30d' }).period).toBe('30d');
  expect(() => overviewSearch({ period: '' })).toThrow(InvalidSearchError);
});

test('比较编号规范成可分享逗号列表，损坏集合不自动删件', () => {
  expect(compareSearch({ ids: [2, 4, 7] })).toEqual({ ids: '2,4,7' });
  expect(comparisonIds('2,4')).toEqual([2, 4]);
  expect(comparisonIds(undefined)).toEqual([]);
  for (const ids of ['2,2', '2,bad', '1,2,3,4,5', '', '1,9007199254740993']) expect(() => comparisonIds(ids)).toThrow(InvalidSearchError);
});

test('打开、翻到相邻详情和关闭保留列表位置，真正筛选变化才重置', () => {
  const list = { task_id: 3, q: 'mini', cursor: 'opaque', sort: 'latest' } as const;
  expect(resultsContextUnchanged(list, { ...list, finding_id: 12 })).toBe(true);
  expect(resultsContextUnchanged({ ...list, finding_id: 12 }, { ...list, finding_id: 15 })).toBe(true);
  expect(resultsContextUnchanged({ ...list, finding_id: 15 }, { ...list, finding_id: undefined })).toBe(true);
  expect(resultsContextUnchanged(list, { ...list, q: 'mini 7' })).toBe(false);
  expect(resultsContextUnchanged(list, { ...list, cursor: 'next' })).toBe(false);
});
