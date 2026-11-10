import { describe, expect, test } from 'bun:test';
import type { Schema } from '../../api/types';
import { changedTaskPatch, draftFromFields, initialProfile, moneyInput, parseMoney, prepareOverrides, prepareTask, profileDraft, profileInput, profileRow, searchChanged, splitWords, suggestedSearchPrice } from './model';

export function taskFields(overrides: Partial<Schema<'TaskFields'>> = {}): Schema<'TaskFields'> {
  return {
    name: '', keyword: '', enabled: true, account_id: null, account_pinned: false,
    min_price: null, max_price: null, personal_only: true, free_shipping_only: false,
    publish_window: 'any', region_province: null, region_city: null, region_district: null,
    max_pages: 1, interval_minutes: 30, crawl_overrides: {}, judge_mode: 'keyword', requirement: '', profile: {},
    budget_min: null, budget_max: null, match_keywords: [], exclude_keywords: [], analyze_on_first_run: true,
    notify_policy: 'recommended', notify_price_drop: true, channel_ids: [], ...overrides,
  };
}

describe('任务表单提交边界', () => {
  test('金额保留空值和零，精确换成分，不接受多余小数、负数和不安全整数', () => {
    expect(parseMoney('')).toBeNull();
    expect(parseMoney('0')).toBe(0);
    expect(parseMoney('29.01')).toBe(2901);
    expect(parseMoney('1.2')).toBe(120);
    expect(parseMoney('1.001')).toBeUndefined();
    expect(parseMoney('-1')).toBeUndefined();
    expect(parseMoney('90071992547409.92')).toBeUndefined();
    expect(parseMoney('90071992547409.91')).toBe(Number.MAX_SAFE_INTEGER);
    expect(moneyInput(2901)).toBe('29.01');
    expect(moneyInput(0)).toBe('0');
  });

  test('新建省略空名称交给后端按rune默认，用户显式超长不截断', () => {
    const draft = draftFromFields(taskFields({ keyword: '𠀀'.repeat(40) }));
    const result = prepareTask(draft);
    expect(result.issues).toEqual([]);
    expect(result.body?.keyword).toHaveLength(80);
    expect(result.body && Object.hasOwn(result.body, 'name')).toBe(false);
    const invalid = prepareTask({ ...draft, name: '𠀀'.repeat(31) });
    expect(invalid.body).toBeNull();
    expect(invalid.issues).toContainEqual({ field: 'name', reason: 'name_length' });
  });

  test('预算搜索价按整数分四舍五入到10元，只省略无效或越界的派生建议', () => {
    const cases: readonly [number | null | undefined, number | null][] = [
      [null, null], [undefined, null], [NaN, null], [Infinity, null], [-1, null], [1.5, null], [Number.MAX_SAFE_INTEGER + 1, null],
      [0, 0], [1249, 1000], [1250, 2000], [1251, 2000], [300000, 360000],
      [7505999378950416, 9007199254740000], [7505999378950417, null], [Number.MAX_SAFE_INTEGER, null],
    ];
    for (const [budget, expected] of cases) expect(suggestedSearchPrice(budget)).toBe(expected);
  });

  test('自动账号实际绑定ID、空渠道和nullable价格原样传递，不改变账号固定方式', () => {
    const draft = draftFromFields(taskFields({ keyword: '相机', account_id: 17, account_pinned: false, channel_ids: [], min_price: 0 }));
    const result = prepareTask(draft);
    expect(result.body).toMatchObject({ keyword: '相机', account_id: 17, account_pinned: false, channel_ids: [], min_price: 0, max_price: null });
    expect(result.body && Object.hasOwn(result.body, 'id')).toBe(false);
  });

  test('超推荐范围可保存，非法数值和范围拒绝，任务覆盖只允许九项', () => {
    const draft = draftFromFields(taskFields({ keyword: '镜头' }));
    const valid = prepareTask({ ...draft, max_pages: '4', interval_minutes: '1', crawl_overrides: { interval_jitter_percent: '0', page_wait_seconds: { min: '10', max: '15' } } });
    expect(valid.body).toMatchObject({ max_pages: 4, interval_minutes: 1, crawl_overrides: { interval_jitter_percent: 0, page_wait_seconds: { min: 10, max: 15 } } });
    const invalid = prepareTask({ ...draft, max_price: '20', min_price: '30', interval_minutes: '0', crawl_overrides: { account_hourly_visits: 100, detail_stay_seconds: { min: '30', max: '10' } } });
    expect(invalid.body).toBeNull();
    expect(invalid.issues).toContainEqual({ field: 'max_price', reason: 'range' });
    expect(invalid.issues).toContainEqual({ field: 'crawl_overrides.account_hourly_visits', reason: 'option' });
    expect(invalid.issues).toContainEqual({ field: 'crawl_overrides.detail_stay_seconds', reason: 'range' });
  });

  test('AI至少需要规格或必须满足，清单固定编号、语义版本和手改来源在提交时保留', () => {
    const fields = taskFields({ keyword: '相机', judge_mode: 'ai', notify_policy: 'good' });
    const draft = draftFromFields(fields);
    expect(prepareTask(draft).issues).toContainEqual({ field: 'profile', reason: 'profile' });
    draft.profile.musts.push(profileRow({ id: 'm7', rev: 3, text: '国行', source: 'user', edited: true }));
    draft.profile.next_no = 11;
    draft.profile.risks.push(profileRow({ id: 'c9', rev: 2, name: '镜头起雾', hint: '镜头内部起雾', question: '', source: 'ai', edited: true, attitude: 'reject' }));
    const result = prepareTask(draft);
    expect(result.body?.profile).toMatchObject({ next_no: 11, musts: [{ id: 'm7', rev: 3, text: '国行', edited: true }], risks: [{ id: 'c9', rev: 2, source: 'ai', edited: true, attitude: 'reject' }] });
  });

  test('任务等待区间接受正有限小数，打字间隔同样按区间；整数覆盖仍拒绝小数和不安全整数', () => {
    expect(prepareOverrides({ page_wait_seconds: { min: '0.5', max: '1.75' }, typing_interval_ms: { min: 0.5, max: 1.75 } })).toEqual({ values: { page_wait_seconds: { min: 0.5, max: 1.75 }, typing_interval_ms: { min: 0.5, max: 1.75 } }, issues: [] });
    for (const min of ['', 'NaN', 'Infinity', '错误数字', -1, 0, Infinity, NaN]) {
      expect(prepareOverrides({ page_wait_seconds: { min, max: 2 } }).issues).toEqual([{ field: 'crawl_overrides.page_wait_seconds', reason: 'invalid_range' }]);
    }
    for (const value of [0.5, '1.75', Number.MAX_SAFE_INTEGER + 1]) {
      expect(prepareOverrides({ max_details_per_run: value }).issues).toEqual([{ field: 'crawl_overrides.max_details_per_run', reason: 'positive_integer' }]);
    }
  });

  test('切到关键词时实际提交空清单，不把隐藏的AI条件发出去', () => {
    const draft = draftFromFields(taskFields({ keyword: '相机' }));
    draft.profile.musts.push(profileRow({ id: 'm1', rev: 1, text: '国行', source: 'user', edited: true }));
    expect(prepareTask(draft).body?.profile).toEqual({});
  });

  test('规格values为空是都行，增删行不通过数组索引改掉固定编号', () => {
    const draft = profileDraft({ summary: '', specs: [{ name: '型号', options: ['mini 6', 'mini 7'], values: [], source: 'user', edited: true }], musts: [{ id: 'm8', rev: 2, text: '国行', source: 'user', edited: false }], bonuses: [], risks: [], allowed_kinds: [], next_no: 12 });
    draft.bonuses.push(profileRow({ id: '', rev: 1, text: '有发票', source: 'user', edited: true }));
    const result = profileInput(draft);
    expect(result.specs?.[0]?.values).toEqual([]);
    expect(result.musts?.[0]?.id).toBe('m8');
    expect(result.bonuses?.[0]?.id).toBe('');
    expect(result.next_no).toBe(12);
  });

  test('通用项只由初次进入AI清单带入，普通序列化不把用户删除的项补回', () => {
    const dictionary: Schema<'AnalysisRiskDefinition'>[] = [
      { id: 'off_platform', name: '站外交易', category: '交易方式', default_attitude: 'reject', hint: '要求站外交易', question: '' },
      { id: 'id_lock', name: 'ID 锁', category: '锁和账号', default_attitude: 'confirm', hint: '账号未退出', question: '账号退出了吗？' },
    ];
    const draft = initialProfile(dictionary);
    expect(draft.risks.map((row) => row.value.id)).toEqual(['off_platform']);
    draft.risks = [];
    expect(profileInput(draft).risks).toEqual([]);
  });

  test('只有规格文档指定的搜索条件触发基线说明，页数、预算和间隔不触发', () => {
    const fields = taskFields({ keyword: '相机' });
    const draft = draftFromFields(fields);
    expect(searchChanged(fields, { ...draft, max_pages: '3', interval_minutes: '15', budget_max: '2000' })).toBe(false);
    expect(searchChanged(fields, { ...draft, max_price: '2000' })).toBe(true);
    expect(searchChanged(fields, { ...draft, free_shipping_only: true })).toBe(true);
  });

  test('匹配与排除词按文档分隔，大小写和全角半角去重，不把空格当分词符', () => {
    expect(splitWords('ＮＶＭｅ，nvme\nmini 6, mini 6\n 国行 ')).toEqual(['ＮＶＭｅ', 'mini 6', '国行']);
  });

  test('PATCH省略未编辑字段，结构对象的键顺序不构成清单修改', () => {
    const profile: Schema<'AnalysisProfileFields'> = { summary: '', specs: [], musts: [{ id: 'm1', rev: 2, text: '国行', source: 'user', edited: true }], bonuses: [], risks: [], allowed_kinds: [], next_no: 2 };
    const base = taskFields({ name: '原名', keyword: '相机', judge_mode: 'ai', notify_policy: 'good', account_id: 17, account_pinned: false, channel_ids: [4, 8], profile });
    const draft = { ...draftFromFields(base), name: '新名字' };
    const result = prepareTask(draft);
    if (!result.body) throw new Error('合法草稿不应失败');
    expect(changedTaskPatch(base, result.body)).toEqual({ name: '新名字' });
    draft.profile.musts[0] = profileRow({ id: 'm1', rev: 2, text: '日版', source: 'user', edited: true });
    const changed = prepareTask(draft);
    if (!changed.body) throw new Error('合法草稿不应失败');
    expect(changedTaskPatch(base, changed.body)).toMatchObject({ name: '新名字', judge_mode: 'ai', profile: { musts: [{ id: 'm1', rev: 2, text: '日版' }] } });
    expect(Object.hasOwn(changedTaskPatch(base, changed.body), 'channel_ids')).toBe(false);
  });
});
