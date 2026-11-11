import { expect, test } from 'bun:test';
import { formatDateTime, formatInteger, formatMoney, formatSellerName } from './format';
import ja from '../../../internal/locales/ja.json';

test.each(['zh-CN', 'en', 'ja'] as const)('%s 缺失昵称显示已有提示，真实昵称原样保留且不依赖编号', (locale) => {
  const missingLabel = locale === 'ja' ? (ja as Record<string, string>)['web.settings.unnamed_seller']!
    : locale === 'zh-CN' ? '未取得卖家昵称' : 'Seller name unavailable';
  for (const name of [null, undefined, '', ' \t\n']) {
    expect(formatSellerName(name, locale)).toBe(missingLabel);
    expect(formatSellerName(name, locale, '列表中的真实名字')).toBe('列表中的真实名字');
  }
  expect(formatSellerName('  原始昵称  ', locale, '列表昵称')).toBe('  原始昵称  ');
  expect(formatSellerName('', locale, '')).toBe(missingLabel);
});

test('金额从整数分显示人民币，数字不受语言转换影响原始值', () => {
  expect(formatMoney(123456, 'en')).toContain('1,234.56');
  expect(formatMoney(0, 'zh-CN')).toContain('0.00');
  expect(formatMoney(-125, 'en')).toContain('1.25');
  expect(formatMoney(Number.MAX_SAFE_INTEGER, 'en')).toContain('90,071,992,547,409.91');
  expect(formatMoney(-1, 'en')).toContain('-CN¥0.01');
  expect(formatInteger(12345, 'en')).toBe('12,345');
  expect(formatMoney(123456, 'ja')).toBe(new Intl.NumberFormat('ja', { style: 'currency', currency: 'CNY', minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(1234.56));
  expect(formatInteger(12345, 'ja')).toBe('12,345');
  for (const value of [0.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1]) {
    expect(() => formatMoney(value, 'en')).toThrow(RangeError);
    expect(() => formatInteger(value, 'en')).toThrow(RangeError);
  }
});

test('UTC 毫秒按显式时区显示，不用浏览器本地时间冒充服务端设置', () => {
  expect(formatDateTime(0, 'en', 'UTC')).toContain('00:00:00');
  expect(formatDateTime(0, 'en', 'Asia/Shanghai')).toContain('08:00:00');
  expect(formatDateTime(0, 'zh-CN', 'UTC')).toContain('1970');
  expect(formatDateTime(0, 'ja', 'Asia/Tokyo')).toContain('09:00:00');
  expect(() => formatDateTime(0.1, 'en', 'UTC')).toThrow(RangeError);
  expect(() => formatDateTime(Number.MAX_SAFE_INTEGER, 'en', 'UTC')).toThrow(RangeError);
  expect(() => formatDateTime(0, 'en', 'unknown-zone')).toThrow(RangeError);
});
