import type { Schema } from '../../api/types';
import { ApiError } from '../../api/errors';
import { useApiError, useMessageText } from '../../api/hooks';
import { formatDateTime, formatInteger } from '../../i18n/format';
import { useTranslation } from '../../i18n';

export const failureKinds = ['risk_verify', 'risk_deny', 'risk_env', 'login', 'network', 'page', 'timeout', 'quota', 'config', 'cancelled', 'restart'] as const satisfies readonly Schema<'FailureKind'>[];

export function useLogError() {
  const errorText = useApiError();
  const messageText = useMessageText();
  const { t } = useTranslation();
  return (error: unknown) => {
    const text = errorText(error);
    if (!(error instanceof ApiError)) return text;
    return [text, ...error.fields.map((field) => messageText(field.reason)), error.requestID ? t('web.request_id', { request_id: error.requestID }) : ''].filter(Boolean).join(' · ');
  };
}

export function useLogFormat(timezone: string | null) {
  const { t, locale } = useTranslation();
  return {
    number: (value: number | null) => value === null ? '—' : formatInteger(value, locale),
    time: (value: number | null) => value === null ? '—' : timezone === null
      ? new Date(value).toISOString()
      : formatDateTime(value, locale, timezone),
    hour: (value: number) => new Intl.DateTimeFormat(locale, {
      timeZone: timezone ?? 'UTC', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
      hourCycle: 'h23', timeZoneName: 'shortOffset',
    }).format(value),
    duration: (value: number | null) => value === null ? '—'
      : `${new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(value / 1000)} ${t('units.seconds', { count: value / 1000 })}`,
    rate: (value: number | null) => value === null ? '—'
      : new Intl.NumberFormat(locale, { style: 'percent', maximumFractionDigits: 1 }).format(value),
  };
}

export function utcInput(value: number | undefined): string {
  return value === undefined ? '' : new Date(value).toISOString().slice(0, -1);
}

export function utcValue(value: string): number | undefined {
  if (!value) return undefined;
  const parsed = Date.parse(`${value}Z`);
  return Number.isFinite(parsed) ? parsed : undefined;
}

export function runDuration(run: Schema<'RunView'>): number | null {
  if (run.outcome === 'skipped' || run.started_at === null || run.ended_at === null) return null;
  const value = run.ended_at - run.started_at;
  return value >= 0 ? value : null;
}
