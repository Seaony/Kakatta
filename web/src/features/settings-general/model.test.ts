import { describe, expect, test } from 'bun:test';
import { definitions, snapshot } from './fixtures.test';
import { buildPatch, draftWarnings, makeDraft, parseDraft, settingOwner } from './model';

function definition(key: string) {
  const value = definitions.find((item) => item.key === key);
  if (!value) throw new Error(key);
  return value;
}

describe('设置草稿与元数据归属', () => {
  test('50项各有唯一页面归属，26项抓取和9项覆盖不遗漏', () => {
    expect(definitions).toHaveLength(50);
    expect(definitions.every((item) => settingOwner(item.key) !== null)).toBe(true);
    expect(definitions.filter((item) => item.group === 'crawl')).toHaveLength(26);
    expect(definitions.filter((item) => item.task_override)).toHaveLength(9);
    expect(settingOwner('crawl.quiet_hours')).toBe('general');
    expect(settingOwner('general.public_url')).toBe('general');
    expect(settingOwner('security.trusted_proxies')).toBe('security');
    expect(settingOwner('general.onboarding_done')).toBe('onboarding');
  });

  test('补丁只发送变更键，空白、区间倒序和超出精确范围的整数保留草稿报错', () => {
    const current = snapshot();
    const patch = buildPatch(definitions, {
      'general.public_url': { kind: 'scalar', text: '' },
      'general.timezone': { kind: 'scalar', text: 'Europe/London' },
      'crawl.failure_threshold': { kind: 'scalar', text: '9007199254740993' },
      'crawl.max_pages': { kind: 'scalar', text: '' },
      'crawl.page_wait_seconds': { kind: 'range', min: '9', max: '3' },
    }, current);
    expect(patch.values).toEqual({ 'general.timezone': 'Europe/London' });
    expect(patch.fields.map((issue) => [issue.key, issue.reason.code])).toEqual([
      ['crawl.page_wait_seconds', 'settings.invalid_range'],
      ['crawl.failure_threshold', 'web.settings.number_precision'],
      ['crawl.max_pages', 'settings.integer_required'],
    ].sort((a, b) => definitions.findIndex((item) => item.key === a[0]) - definitions.findIndex((item) => item.key === b[0])));
  });

  test('推荐外不夹值，区间小数原值提交，关闭时段保留原时间', () => {
    const interval = definition('crawl.task_interval_minutes');
    expect(parseDraft(interval, { kind: 'scalar', text: '1' })).toEqual({ ok: true, value: 1 });
    expect(draftWarnings(interval, { kind: 'scalar', text: '1' }).map((item) => item.code)).toEqual(['settings.outside_recommendation']);
    expect(parseDraft(definition('crawl.detail_stay_seconds'), { kind: 'range', min: '0.5', max: '1.75' })).toEqual({ ok: true, value: { min: 0.5, max: 1.75 } });
    expect(parseDraft(definition('crawl.quiet_hours'), { kind: 'quiet_hours', enabled: false, start: '23:00', end: '07:30' })).toEqual({ ok: true, value: { enabled: false, start: '23:00', end: '07:30' } });
    expect(draftWarnings(definition('crawl.quiet_hours'), { kind: 'quiet_hours', enabled: true, start: '07:30', end: '07:30' })[0]?.code).toBe('settings.quiet_disabled');
  });

  test('清空地址用空字符串，永久保留用0，不把空值变成null', () => {
    const current = snapshot();
    current.values['general.public_url'] = 'https://local.test/kakatta';
    const result = buildPatch(definitions, {
      'general.public_url': { kind: 'scalar', text: '' },
      'retention.findings_days': { kind: 'scalar', text: '0' },
    }, current);
    expect(result.fields).toEqual([]);
    expect(result.values).toEqual({ 'general.public_url': '', 'retention.findings_days': 0 });
  });

  test('缺少值不伪造默认，环境只读项不进入补丁', () => {
    expect(makeDraft(definition('general.timezone'), undefined)).toBeNull();
    const current = snapshot();
    current.sources['general.timezone'] = 'environment';
    expect(buildPatch(definitions, { 'general.timezone': { kind: 'scalar', text: 'UTC' } }, current).values).toEqual({});
  });
});
