import type { Locale } from '../../i18n';

export function formatStatisticMoney(cents: number, locale: Locale): string {
  if (!Number.isFinite(cents)) throw new RangeError('统计金额必须是有限数值');
  return new Intl.NumberFormat(locale, {
    style: 'currency', currency: 'CNY', minimumFractionDigits: 2, maximumFractionDigits: 20,
  }).format(cents / 100);
}

export function formatMarketPercent(ratio: number, locale: Locale): string {
  return new Intl.NumberFormat(locale, {
    style: 'percent', maximumFractionDigits: 20, signDisplay: 'exceptZero',
  }).format(ratio);
}

export function safeListingURL(value: string): string | undefined {
  try {
    const url = new URL(value);
    return url.protocol === 'https:' || url.protocol === 'http:' ? url.href : undefined;
  } catch {
    return undefined;
  }
}
