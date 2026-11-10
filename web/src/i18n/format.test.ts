import { expect, test } from 'bun:test';
import { formatDateTime, formatInteger, formatMoney } from './format';

test('金额从整数分显示人民币，数字不受语言转换影响原始值', () => {
  expect(formatMoney(123456, 'en')).toContain('1,234.56');
  expect(formatMoney(0, 'zh-CN')).toContain('0.00');
  expect(formatMoney(-125, 'en')).toContain('1.25');
  expect(formatMoney(Number.MAX_SAFE_INTEGER, 'en')).toContain('90,071,992,547,409.91');
  expect(formatMoney(-1, 'en')).toContain('-CN¥0.01');
  expect(formatInteger(12345, 'en')).toBe('12,345');
  for (const value of [0.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1]) {
    expect(() => formatMoney(value, 'en')).toThrow(RangeError);
    expect(() => formatInteger(value, 'en')).toThrow(RangeError);
  }
});

test('UTC 毫秒按显式时区显示，不用浏览器本地时间冒充服务端设置', () => {
  expect(formatDateTime(0, 'en', 'UTC')).toContain('00:00:00');
  expect(formatDateTime(0, 'en', 'Asia/Shanghai')).toContain('08:00:00');
  expect(formatDateTime(0, 'zh-CN', 'UTC')).toContain('1970');
  expect(() => formatDateTime(0.1, 'en', 'UTC')).toThrow(RangeError);
  expect(() => formatDateTime(Number.MAX_SAFE_INTEGER, 'en', 'UTC')).toThrow(RangeError);
  expect(() => formatDateTime(0, 'en', 'unknown-zone')).toThrow(RangeError);
});
