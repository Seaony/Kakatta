import { useId } from 'react';
import type { components } from '../../api/schema.gen';
import { useTranslation } from '../../i18n';
import { formatInteger } from '../../i18n/format';
import { formatStatisticMoney } from './format';

type DailyPrice = components['schemas']['AnalysisDailyPrice'];

export function DailyPrices({ config, points }: { config: string; points: readonly DailyPrice[] }) {
  const { t, locale } = useTranslation();
  const titleId = useId();
  const amounts = points.flatMap((point) => point.median === null ? [] : [point.median]);
  const low = amounts.length > 0 ? Math.min(...amounts) : 0;
  const high = amounts.length > 0 ? Math.max(...amounts) : 0;
  const range = high - low || 1;
  const x = (index: number) => 24 + index / Math.max(points.length - 1, 1) * 592;
  const y = (value: number) => high === low ? 90 : 150 - (value - low) / range * 120;
  const segments: string[] = [];
  let drawing = false;
  for (const [index, point] of points.entries()) {
    if (point.median === null) { drawing = false; continue; }
    const coordinate = `${x(index)},${y(point.median)}`;
    if (drawing) segments[segments.length - 1] += ` L${coordinate}`;
    else segments.push(`M${coordinate}`);
    drawing = true;
  }

  return <section aria-labelledby={titleId} className="min-w-0 space-y-md">
    <div className="flex flex-wrap items-baseline justify-between gap-sm">
      <h3 id={titleId} className="text-section font-semibold">{t('web.market.daily_title')}</h3>
      <p className="break-words text-body text-text-secondary">{config}</p>
    </div>
    {amounts.length === 0 ? <p className="py-lg text-text-secondary">{t('web.market.trend_unavailable')}</p> : <>
      <div className="flex justify-between gap-md text-caption tabular-nums text-text-secondary">
        <span>{formatStatisticMoney(low, locale)}</span><span>{formatStatisticMoney(high, locale)}</span>
      </div>
      <svg viewBox="0 0 640 180" role="img" aria-label={t('web.market.daily_table', { config })} className="block w-full overflow-visible text-accent-text">
        <title>{t('web.market.daily_table', { config })}</title>
        <line x1="24" x2="616" y1="150" y2="150" stroke="rgb(var(--color-border) / var(--opacity-border))" />
        {segments.map((path, index) => <path key={index} d={path} fill="none" stroke="currentColor" strokeWidth="2" vectorEffect="non-scaling-stroke" />)}
        {points.map((point, index) => point.median === null ? null : <circle key={point.day} cx={x(index)} cy={y(point.median)} r="3.5" fill="currentColor">
          <title>{`${point.day}: ${formatStatisticMoney(point.median, locale)} · ${t('web.market.samples_count', { count: point.sample })}`}</title>
        </circle>)}
      </svg>
      <div className="flex justify-between gap-md text-caption tabular-nums text-text-secondary"><span>{points[0]?.day}</span><span>{points.at(-1)?.day}</span></div>
    </>}
    <details className="min-w-0">
      <summary className="min-h-control cursor-pointer rounded-control py-md text-body font-medium text-text-label">{t('web.market.view_data')}</summary>
      <div className="overflow-x-auto">
        <table className="w-full text-body tabular-nums">
          <caption className="sr-only">{t('web.market.daily_table', { config })}</caption>
          <thead><tr className="border-b border-border text-text-secondary">
            <th scope="col" className="py-md pr-lg text-left font-medium">{t('web.market.date')}</th>
            <th scope="col" className="p-md text-right font-medium">{t('web.market.median')}</th>
            <th scope="col" className="py-md pl-lg text-right font-medium">{t('web.market.sample_count')}</th>
          </tr></thead>
          <tbody>{points.map((point) => <tr key={point.day} className="border-b border-border">
            <th scope="row" className="py-md pr-lg text-left font-normal">{point.day}</th>
            <td className="p-md text-right">{point.median === null ? t('web.market.no_daily_sample') : formatStatisticMoney(point.median, locale)}</td>
            <td className="py-md pl-lg text-right">{formatInteger(point.sample, locale)}</td>
          </tr>)}</tbody>
        </table>
      </div>
    </details>
  </section>;
}
