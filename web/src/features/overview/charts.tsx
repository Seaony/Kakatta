import { useId } from 'react';
import type { components } from '../../api/schema.gen';
import { useTranslation } from '../../i18n';
import { formatInteger } from '../../i18n/format';
import { bucketTime, statisticalPrice } from './format';

type ActivityBucket = components['schemas']['ActivityBucket'];
type DailyPrice = components['schemas']['AnalysisDailyPrice'];

const cellClass = 'border-b border-border px-md py-sm text-right tabular-nums';
const tableClass = 'w-full border-collapse text-body';
const summaryClass = 'min-h-control cursor-pointer rounded-control py-sm text-body font-medium text-accent-text underline-offset-4 focus-visible:underline';

export function ActivityChart({ buckets, timezone, asOf }: {
  buckets: readonly ActivityBucket[]; timezone: string; asOf: number;
}) {
  const { t, locale } = useTranslation();
  const titleID = useId();
  const maximum = Math.max(1, ...buckets.map((bucket) => Math.max(bucket.new_count, bucket.seen_count)));
  const column = 600 / Math.max(1, buckets.length);
  const hasActivity = buckets.some((bucket) => bucket.new_count > 0 || bucket.seen_count > 0);
  const first = buckets[0];
  const last = buckets[buckets.length - 1];
  return (
    <div className="min-w-0 space-y-md">
      <div className="flex flex-wrap gap-x-lg gap-y-sm text-body">
        <span className="inline-flex items-center gap-sm"><span aria-hidden="true" className="h-sm w-sm bg-positive" />{t('overview.new_count')}</span>
        <span className="inline-flex items-center gap-sm"><span aria-hidden="true" className="h-sm w-sm bg-info" />{t('overview.seen_count')}</span>
      </div>
      {!hasActivity && <p className="text-body text-text-secondary">{t('overview.activity_empty')}</p>}
      {buckets.length > 0 && (
        <>
          <svg viewBox="0 0 600 160" className="block w-full text-text-secondary" role="img" aria-labelledby={titleID}>
            <title id={titleID}>{t('overview.activity')}</title>
            <line x1="0" y1="144" x2="600" y2="144" stroke="currentColor" strokeOpacity="0.25" />
            {buckets.map((bucket, index) => {
              const newHeight = bucket.new_count / maximum * 128;
              const seenHeight = bucket.seen_count / maximum * 128;
              const future = bucket.from_at >= asOf;
              return <g key={`${bucket.from_at}:${bucket.to_at}`} opacity={future ? 0.25 : 1}>
                <title>{bucketTime(bucket.from_at, locale, timezone)}; {t('overview.new_count')}: {future ? t('overview.not_yet') : formatInteger(bucket.new_count, locale)}; {t('overview.seen_count')}: {future ? t('overview.not_yet') : formatInteger(bucket.seen_count, locale)}</title>
                <rect x={index * column + column * 0.12} y={144 - newHeight} width={column * 0.32} height={newHeight} fill="rgb(var(--color-positive))" />
                <rect x={index * column + column * 0.52} y={144 - seenHeight} width={column * 0.32} height={seenHeight} fill="rgb(var(--color-info))" />
              </g>;
            })}
          </svg>
          <div aria-hidden="true" className="flex justify-between gap-lg text-caption text-text-secondary">
            <span>{first && bucketTime(first.from_at, locale, timezone)}</span>
            <span className="text-right">{last && bucketTime(last.to_at, locale, timezone)}</span>
          </div>
        </>
      )}
      <details>
        <summary className={summaryClass}>{t('overview.activity_data')}</summary>
        <div className="max-w-full overflow-x-auto">
          <table className={tableClass}>
            <caption className="sr-only">{t('overview.activity')}</caption>
            <thead><tr><th scope="col" className="border-b border-border px-md py-sm text-left">{t('overview.bucket')}</th><th scope="col" className={cellClass}>{t('overview.new_count')}</th><th scope="col" className={cellClass}>{t('overview.seen_count')}</th></tr></thead>
            <tbody>{buckets.map((bucket) => <tr key={`${bucket.from_at}:${bucket.to_at}`}>
              <th scope="row" className="border-b border-border px-md py-sm text-left font-normal">
                <time dateTime={new Date(bucket.from_at).toISOString()}>{bucketTime(bucket.from_at, locale, timezone)}</time>
                <span className="block text-caption text-text-secondary"><time dateTime={new Date(bucket.to_at).toISOString()}>{bucketTime(bucket.to_at, locale, timezone)}</time></span>
              </th>
              <td className={cellClass}>{bucket.from_at >= asOf ? t('overview.not_yet') : formatInteger(bucket.new_count, locale)}</td>
              <td className={cellClass}>{bucket.from_at >= asOf ? t('overview.not_yet') : formatInteger(bucket.seen_count, locale)}</td>
            </tr>)}</tbody>
          </table>
        </div>
      </details>
    </div>
  );
}

export function PriceTrend({ days, name }: { days: readonly DailyPrice[]; name: string }) {
  const { t, locale } = useTranslation();
  const titleID = useId();
  const values = days.flatMap((day) => day.median === null ? [] : [day.median]);
  const lower = values.length > 0 ? Math.min(...values) : 0;
  const span = values.length > 0 ? Math.max(...values) - lower : 0;
  const point = (index: number, value: number) => ({
    x: 8 + index / Math.max(1, days.length - 1) * 284,
    y: span === 0 ? 40 : 70 - (value - lower) / span * 60,
  });
  return <div className="min-w-0">
    {values.length === 0 ? <p className="text-body text-text-secondary">{t('overview.trend_empty')}</p> : (
      <svg viewBox="0 0 300 80" preserveAspectRatio="none" role="img" aria-labelledby={titleID} className="block h-[var(--space-xxl)] w-full overflow-visible text-info">
        <title id={titleID}>{t('overview.trend_data', { name })}</title>
        {days.map((day, index) => {
          if (day.median === null) return null;
          const here = point(index, day.median);
          const previous = days[index - 1];
          const before = previous?.median !== null && previous?.median !== undefined ? point(index - 1, previous.median) : null;
          return <g key={`${day.day}:${index}`}>
            {before && <line x1={before.x} y1={before.y} x2={here.x} y2={here.y} stroke="currentColor" strokeWidth="2" vectorEffect="non-scaling-stroke" />}
            <circle cx={here.x} cy={here.y} r="2.5" fill="currentColor"><title>{day.day}: {statisticalPrice(day.median, locale)}</title></circle>
          </g>;
        })}
      </svg>
    )}
    <details className="mt-sm">
      <summary className={summaryClass}>{t('overview.trend_data', { name })}</summary>
      <div className="max-w-full overflow-x-auto">
        <table className={tableClass}>
          <caption className="sr-only">{t('overview.trend')}</caption>
          <thead><tr><th scope="col" className="border-b border-border px-md py-sm text-left">{t('overview.day')}</th><th scope="col" className={cellClass}>{t('overview.median')}</th><th scope="col" className={cellClass}>{t('overview.sample')}</th></tr></thead>
          <tbody>{days.map((day, index) => <tr key={`${day.day}:${index}`}><th scope="row" className="border-b border-border px-md py-sm text-left font-normal">{day.day}</th><td className={cellClass}>{day.median === null ? t('overview.unknown') : statisticalPrice(day.median, locale)}</td><td className={cellClass}>{formatInteger(day.sample, locale)}</td></tr>)}</tbody>
        </table>
      </div>
    </details>
  </div>;
}
