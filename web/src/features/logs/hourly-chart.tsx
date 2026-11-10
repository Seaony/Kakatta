import { useId } from 'react';
import type { Schema } from '../../api/types';
import { useTranslation } from '../../i18n';
import { useLogFormat } from './display';

const series = [
  { key: 'success_count', label: 'logs.outcome.success', fill: 'fill-positive', text: 'text-positive' },
  { key: 'failed_count', label: 'logs.outcome.failed', fill: 'fill-danger', text: 'text-danger' },
  { key: 'skipped_count', label: 'logs.outcome.skipped', fill: 'fill-text-secondary', text: 'text-text-secondary' },
] as const;

export function HourlyChart({ buckets, timezone }: { buckets: Schema<'RunHourBucket'>[]; timezone: string | null }) {
  const { t } = useTranslation();
  const format = useLogFormat(timezone);
  const titleID = useId();
  const maximum = Math.max(1, ...buckets.map((bucket) => bucket.success_count + bucket.failed_count + bucket.skipped_count));
  const width = 720;
  const height = 144;
  const slot = width / Math.max(1, buckets.length);
  return (
    <section aria-labelledby={titleID} className="space-y-md">
      <div className="flex flex-wrap items-baseline justify-between gap-md">
        <h3 id={titleID} className="text-body font-medium">{t('logs.chart.title')}</h3>
        <ul className="flex flex-wrap gap-lg text-caption">
          {series.map((item) => <li key={item.key} className={item.text}>{t(item.label)}</li>)}
        </ul>
      </div>
      <svg viewBox={`0 0 ${width} ${height}`} className="h-auto w-full overflow-visible" role="img" aria-label={t('logs.chart.title')}>
        <line x1="0" x2={width} y1={height - 1} y2={height - 1} className="stroke-border" />
        {buckets.map((bucket, index) => {
          let offset = 0;
          return <g key={`${bucket.from_at}:${bucket.to_at}`}>
            <title>{`${format.hour(bucket.from_at)} – ${format.hour(bucket.to_at)}: ${series.map((item) => `${t(item.label)} ${format.number(bucket[item.key])}`).join(', ')}`}</title>
            {series.map((item) => {
              const barHeight = bucket[item.key] / maximum * (height - 12);
              const y = height - 1 - offset - barHeight;
              offset += barHeight;
              return <rect key={item.key} x={index * slot + 3} y={y} width={Math.max(1, slot - 6)} height={barHeight} className={item.fill} />;
            })}
          </g>;
        })}
      </svg>
      {buckets.length > 0 && <div aria-hidden="true" className="flex justify-between gap-md text-caption text-text-secondary tabular-nums"><span>{format.hour(buckets[0]!.from_at)}</span><span>{format.hour(buckets[buckets.length - 1]!.to_at)}</span></div>}
      <details className="text-body">
        <summary className="flex min-h-control cursor-pointer items-center rounded-control text-text-label focus-visible:outline focus-visible:outline-2 focus-visible:outline-focus">{t('logs.chart.table')}</summary>
        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-body tabular-nums">
            <caption className="sr-only">{t('logs.chart.title')}</caption>
            <thead><tr className="border-b border-border"><th scope="col" className="py-md pr-lg text-left font-medium">{t('logs.chart.hour')}</th>{series.map((item) => <th scope="col" key={item.key} className="px-md py-md text-right font-medium">{t(item.label)}</th>)}</tr></thead>
            <tbody>{buckets.map((bucket) => <tr key={`${bucket.from_at}:${bucket.to_at}`} className="border-b border-border"><th scope="row" className="py-md pr-lg text-left font-normal">{format.hour(bucket.from_at)} – {format.hour(bucket.to_at)}</th>{series.map((item) => <td key={item.key} className="px-md py-md text-right">{format.number(bucket[item.key])}</td>)}</tr>)}</tbody>
          </table>
        </div>
      </details>
    </section>
  );
}
