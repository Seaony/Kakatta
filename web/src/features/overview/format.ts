import type { Locale } from '../../i18n';

export function percent(value: number, locale: Locale, signed = false): string {
  return new Intl.NumberFormat(locale, {
    style: 'percent', maximumFractionDigits: 1,
    ...(signed ? { signDisplay: 'always' as const } : {}),
  }).format(value);
}

export function signedInteger(value: number, locale: Locale): string {
  return new Intl.NumberFormat(locale, { signDisplay: 'always', maximumFractionDigits: 0 }).format(value);
}

export function decimal(value: number, locale: Locale): string {
  return new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(value);
}

export function statisticalPrice(cents: number, locale: Locale): string {
  return new Intl.NumberFormat(locale, {
    style: 'currency', currency: 'CNY', minimumFractionDigits: 2, maximumFractionDigits: 20,
  }).format(cents / 100);
}

export function bucketTime(at: number, locale: Locale, timezone: string): string {
  return new Intl.DateTimeFormat(locale, {
    timeZone: timezone, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
    hourCycle: 'h23', timeZoneName: 'shortOffset',
  }).format(at);
}
