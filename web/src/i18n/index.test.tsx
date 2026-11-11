import { expect, test } from 'bun:test';
import { act, render, renderHook, screen } from '@testing-library/react';
import { createI18n, formatMessage, I18nProvider, selectLocale, setLocale, TranslationError, useTranslation } from './index';
import type { Locale, TranslationKey } from './index';

test('按共享规则替换参数一次，参数中的模板和嵌套引用保持原样', () => {
  expect(formatMessage({ code: 'settings.unknown_field', args: { field: '{next} $t(common.cancel)', next: '不应替换' } }, 'zh-CN'))
    .toBe('不存在字段{next} $t(common.cancel)。');
  expect(formatMessage({ code: 'settings.minimum', args: { min: 0 } }, 'zh-CN')).toBe('此项不能小于0。');
  expect(() => formatMessage({ code: 'settings.unknown_field', args: {} }, 'en')).toThrow(TranslationError);
  expect(() => formatMessage({ code: 'settings.unknown_field', args: { field: null } }, 'en')).toThrow(TranslationError);
  expect(() => formatMessage({ code: 'not-a-message', args: {} }, 'en')).toThrow(TranslationError);
  expect(() => formatMessage({ code: 'common.cancel', args: {} }, 'en-US' as Locale)).toThrow(TranslationError);
});

test.each(['toString', '__proto__'])('原型键 %s 在纯格式化和 React 翻译入口都按未知代码拒绝', (code) => {
  const instance = createI18n('en');
  const hook = renderHook(() => useTranslation(), {
    wrapper: ({ children }) => <I18nProvider instance={instance}>{children}</I18nProvider>,
  });
  const assertUnknown = (run: () => unknown) => {
    let failure: unknown;
    try { run(); } catch (error) { failure = error; }
    expect(failure).toBeInstanceOf(TranslationError);
    expect((failure as TranslationError).kind).toBe('unknown_code');
  };
  for (const locale of ['zh-CN', 'en', 'ja'] as const) assertUnknown(() => formatMessage({ code, args: {} }, locale));
  assertUnknown(() => hook.result.current.t(code as TranslationKey));
  hook.unmount();
});

test('两语言同按有限数值 count 选择单复数，字符串和非数值不能绕过检查', () => {
  for (const [count, expected] of [[1, '1 channel selected'], [0, '0 channels selected'], [2, '2 channels selected'], [-1, '-1 channels selected']] as const) {
    expect(formatMessage({ code: 'settings.selected_channels', args: { count } }, 'en')).toBe(expected);
    expect(formatMessage({ code: 'settings.selected_channels', args: { count } }, 'zh-CN')).toBe(`已选择${count}个渠道`);
  }
  for (const count of ['1', null, undefined, NaN, Infinity]) {
    expect(() => formatMessage({ code: 'settings.selected_channels', args: { count } }, 'en')).toThrow(TranslationError);
  }
});

test('语言实例独立复制资源，严格入口不读取已被调用方修改的映射', async () => {
  const first = createI18n('en');
  const second = createI18n('zh-CN');
  expect(first.isInitialized).toBe(true);
  expect(second.isInitialized).toBe(true);
  first.addResource('en', 'translation', 'common.cancel', 'changed');
  expect(createI18n('en').t('common.cancel')).toBe('Cancel');
  expect(formatMessage({ code: 'common.cancel', args: {} }, 'en')).toBe('Cancel');
  await setLocale(first, 'zh-CN');
  expect(second.language).toBe('zh-CN');
  expect(first.options.keySeparator).toBe(false);
});

function TranslationProbe() {
  const { t, locale } = useTranslation();
  return <p role="status">{locale}:{t('common.cancel')}</p>;
}

test('React 入口响应语言变化并更新 html.lang，卸载后归还原值', async () => {
  const original = document.documentElement.lang;
  const instance = createI18n('zh-CN');
  const rendered = render(<I18nProvider instance={instance}><TranslationProbe /></I18nProvider>);
  expect(screen.getByRole('status').textContent).toBe('zh-CN:取消');
  expect(document.documentElement.lang).toBe('zh-CN');
  await act(async () => { await setLocale(instance, 'en'); });
  expect(screen.getByRole('status').textContent).toBe('en:Cancel');
  expect(document.documentElement.lang).toBe('en');
  await act(async () => { await setLocale(instance, 'ja'); });
  expect(screen.getByRole('status').textContent).toBe('ja:キャンセル');
  expect(document.documentElement.lang).toBe('ja');
  rendered.unmount();
  expect(document.documentElement.lang).toBe(original);
});

test('日语严格格式化保持单次插值、复数与卖家原文，不改模型提供的自由文字', () => {
  const original = '{next} $t(common.cancel) 卖家原话';
  expect(formatMessage({ code: 'settings.unknown_field', args: { field: original } }, 'ja')).toContain(original);
  for (const count of [0, 1, 2]) {
    const value = formatMessage({ code: 'settings.selected_channels', args: { count } }, 'ja');
    expect(value).toContain(String(count));
    expect(value).not.toContain('{count}');
  }
  for (const count of ['1', null, NaN, Infinity]) {
    expect(() => formatMessage({ code: 'settings.selected_channels', args: { count } }, 'ja')).toThrow(TranslationError);
  }
  expect(formatMessage({ code: 'analysis.risk_unknown', args: { id: 'custom_17', name: original } }, 'ja')).toContain(original);
});

test('系统语言、显式偏好和 Accept-Language 权重与服务端约定一致', () => {
  for (const [input, expected] of [
    ['en-US', 'en'], ['zh-TW', 'zh-CN'], ['fr-FR', 'en'], ['de-DE', 'en'],
    ['not_a_locale', 'en'], ['C.UTF-8', 'en'], ['', 'en'], ['en_GB.UTF-8', 'en'],
    ['ja-JP', 'ja'], ['JA-jp', 'ja'], ['ja_JP.UTF-8', 'ja'],
    ['zh-CN;q=.2,en;q=.9', 'en'], ['en;q=0,zh-CN;q=.2', 'zh-CN'],
    ['ja-JP;q=.9,en;q=.5', 'ja'], ['en;q=.9,ja;q=.5', 'en'],
    ['ja;q=0,en;q=.5', 'en'], ['ja;q=broken,zh-CN;q=.5', 'zh-CN'],
    ['fr;q=.9,zh;q=.5', 'zh-CN'], ['ja;q=.8,en;q=.8', 'ja'],
    ['en;q=0,ja;q=0', 'en'], ['fr-FR;q=1', 'en'],
  ] as const) expect(selectLocale('system', input)).toBe(expected);
  expect(selectLocale('en', 'zh-CN')).toBe('en');
  expect(selectLocale('zh-CN', 'en-US')).toBe('zh-CN');
  expect(selectLocale('ja', 'zh-CN')).toBe('ja');
});

test('只有四类风险原因按固定编号翻译名称，参数不被修改', () => {
  const args = Object.freeze({ id: 'id_lock', name: 'ID 锁', attitude: 'confirm' });
  for (const code of ['analysis.risk_confirmed', 'analysis.risk_penalty', 'analysis.risk_uncertain', 'analysis.risk_unknown']) {
    expect(formatMessage({ code, args }, 'en')).toContain('ID lock');
    expect(formatMessage({ code, args }, 'zh-CN')).toContain('ID 锁');
  }
  expect(args.name).toBe('ID 锁');
  expect(formatMessage({ code: 'analysis.risk_unknown', args: { ...args, id: 'custom_17' } }, 'en')).toContain('ID 锁');
  expect(formatMessage({ code: 'analysis.bonus_met', args }, 'en')).toContain('ID 锁');
  expect(formatMessage({ code: 'analysis.risk_unknown', args: { ...args, id: 'custom_17', name: '{name}' } }, 'en')).toContain('{name}');
});
