import type { Locale } from './index';

function requireInteger(value: number) {
  if (!Number.isSafeInteger(value)) throw new RangeError('显示数值必须是安全整数');
}

export function formatMoney(cents: number, locale: Locale): string {
  requireInteger(cents);
  // 先拆整数元和分，避免大金额除以 100 时少显示一分。
  const amount = BigInt(cents);
  const whole = amount / 100n;
  const remainder = amount < 0n ? -amount % 100n : amount % 100n;
  const fraction = remainder.toString().padStart(2, '0');
  const sign = cents < 0 && whole === 0n ? -0 : whole;
  return new Intl.NumberFormat(locale, { style: 'currency', currency: 'CNY', minimumFractionDigits: 2, maximumFractionDigits: 2 })
    .formatToParts(sign).map((part) => part.type === 'fraction' ? fraction : part.value).join('');
}

export function formatInteger(value: number, locale: Locale): string {
  requireInteger(value);
  return new Intl.NumberFormat(locale).format(value);
}

export function formatDateTime(utcMilliseconds: number, locale: Locale, timeZone: string): string {
  requireInteger(utcMilliseconds);
  if (Number.isNaN(new Date(utcMilliseconds).valueOf())) throw new RangeError('显示时间超出日期范围');
  return new Intl.DateTimeFormat(locale, {
    timeZone, year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
  }).format(utcMilliseconds);
}
