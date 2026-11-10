import { useId, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import type { components } from '../../api/schema.gen';
import { apiData, useApi, useApiError, useMessageText } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import { ApiError } from '../../api/errors';
import { Button, Dialog, InlineMessage, Popover, Skeleton, StatePanel, StatusBadge } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { formatDateTime, formatMoney } from '../../i18n/format';
import { ListingPanel } from '../results/listing-panel';
import { DailyPrices } from './daily-prices';
import { formatMarketPercent, formatStatisticMoney } from './format';
import { MarketSamples } from './market-samples';

type MarketGroup = components['schemas']['AnalysisMarketGroup'];
type MarketView = components['schemas']['MarketView'];
type MarketInsight = components['schemas']['MarketInsight'];

function identity(group: MarketGroup) { return JSON.stringify([group.config_key, group.brand_new]); }

export function MarketPanel({ taskId }: { taskId: number }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const errorText = useApiError();
  const { t } = useTranslation();
  const task = useQuery({ queryKey: apiKey('tasks', taskId), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/tasks/{task_id}', { signal, params: { path: { task_id: taskId } } })) });
  const settings = useQuery({ queryKey: apiKey('settings'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings', { signal })) });
  const market = useQuery({ queryKey: apiKey('tasks', taskId, 'market'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/tasks/{task_id}/market', { signal, params: { path: { task_id: taskId } } })) });
  const providers = useQuery({ queryKey: apiKey('ai-providers'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/ai/providers', { signal })) });
  const timezone = settings.data?.values['general.timezone'];
  const failure = market.error ?? task.error;
  const refresh = () => { void Promise.all([queryClient.invalidateQueries({ queryKey: apiKey('tasks', taskId, 'market') }), task.refetch(), settings.refetch(), providers.refetch()]); };

  if ((!market.data || !task.data) && failure) return <StatePanel kind="error" title={t('web.market.loading_failed')} description={[errorText(failure), failure instanceof ApiError && failure.requestID ? t('web.request_id', { request_id: failure.requestID }) : ''].filter(Boolean).join(' ')} retry={{ label: t('common.retry'), run: refresh }} />;
  if (!market.data || !task.data) return <StatePanel kind="pending" label={t('web.market.refreshing')}><Skeleton /><Skeleton /></StatePanel>;
  return <MarketContent key={taskId} taskId={taskId} data={market.data} mode={task.data.judge_mode} timezone={typeof timezone === 'string' ? timezone : undefined} refreshing={market.isFetching} error={failure} settingsError={settings.error} aiConfigured={providers.data === undefined ? undefined : providers.data.length > 0} providersPending={providers.isPending} providersError={providers.error} onRefresh={refresh} />;
}

export function MarketContent({ taskId, data, mode, timezone, refreshing = false, error, settingsError, aiConfigured, providersPending = false, providersError, onRefresh }: {
  taskId: number; data: MarketView; mode: components['schemas']['AnalysisMode']; timezone: string | undefined;
  refreshing?: boolean; error?: unknown; settingsError?: unknown; aiConfigured?: boolean | undefined;
  providersPending?: boolean; providersError?: unknown; onRefresh(): void;
}) {
  const { t, locale } = useTranslation();
  const messageText = useMessageText();
  const errorText = useApiError();
  const headingId = useId();
  const [basisOpen, setBasisOpen] = useState(false);
  const [selected, setSelected] = useState<string>();
  const [samples, setSamples] = useState<MarketGroup>();
  const [listing, setListing] = useState<string>();
  const groups = data.market.groups.filter((group) => group.selected);
  const current = groups.find((group) => identity(group) === selected) ?? groups[0];
  const currentSamples = samples ? data.market.groups.find((group) => identity(group) === identity(samples)) ?? samples : undefined;
  const label = (group: MarketGroup) => `${group.config_key === '*' ? t('web.market.no_configuration') : group.config_key}${mode === 'keyword' ? '' : ` · ${group.brand_new ? t('web.market.new') : t('web.market.used')}`}`;

  return <section aria-labelledby={headingId} className="min-w-0 space-y-xl border-b border-border py-xl">
    <header className="flex flex-wrap items-start justify-between gap-md">
      <div className="min-w-0 space-y-sm">
        <h2 id={headingId} className="text-section font-semibold">{t('web.market.title')}</h2>
        <p className="text-caption text-text-secondary">{data.market.snapshot_at === null ? t('web.market.no_snapshot') : timezone ? t('web.market.snapshot_at', { time: formatDateTime(data.market.snapshot_at, locale, timezone) }) : t('web.market.timezone_unavailable')}</p>
      </div>
      <Button busy={refreshing} busyLabel={t('web.market.refreshing')} onClick={onRefresh}>{t('web.market.refresh')}</Button>
    </header>
    {error ? <InlineMessage tone="warning" action={<Button onClick={onRefresh}>{t('common.retry')}</Button>}><p>{t('web.market.refresh_failed')}</p><p>{errorText(error)}</p>{error instanceof ApiError && error.requestID && <p>{t('web.request_id', { request_id: error.requestID })}</p>}</InlineMessage> : null}
    {settingsError ? <InlineMessage tone="warning">{errorText(settingsError)}</InlineMessage> : null}
    {data.market.warnings.map((warning, index) => <InlineMessage key={index} tone="warning">{messageText(warning)}</InlineMessage>)}
    <div className="flex flex-wrap items-center gap-sm">
      <p className="text-body text-text-secondary">{t('web.market.listing_price_note')}</p>
      <Popover open={basisOpen} onOpenChange={setBasisOpen} label={t('web.market.statistics_basis')} trigger={<Button variant="plain">{t('web.market.statistics_basis')}</Button>}>
        <div className="space-y-md">{mode !== 'keyword' && <p>{t('web.market.statistics_rules')}</p>}<p>{t(mode === 'keyword' ? 'web.market.keyword_help' : 'web.market.relaxation_help')}</p></div>
      </Popover>
    </div>
    {groups.length === 0 ? <StatePanel kind="empty" title={t('web.market.no_samples.title')} description={t(data.market.groups.length > 0 ? 'web.market.no_selected_samples' : 'web.market.no_samples.description')} /> : <div className="divide-y divide-border">
      {groups.map((group) => <article key={identity(group)} aria-label={label(group)} className="min-w-0 space-y-md py-lg">
        <div className="flex flex-wrap items-center justify-between gap-md">
          <h3 className="min-w-0 break-words text-body font-semibold">{label(group)}</h3>
          <Button variant={current && identity(current) === identity(group) ? 'secondary' : 'plain'} aria-pressed={current ? identity(current) === identity(group) : false} aria-label={t('web.market.show_daily', { config: label(group) })} onClick={() => setSelected(identity(group))}>{t('web.market.daily_title')}</Button>
        </div>
        <dl className={`grid grid-cols-1 gap-lg sm:grid-cols-2 ${mode === 'keyword' ? 'lg:grid-cols-2' : 'lg:grid-cols-4'}`}>
          <div className="min-w-0 space-y-sm">
            <dt className="text-caption text-text-secondary">{t(mode === 'keyword' ? 'web.market.median' : 'web.market.fair_price')}</dt>
            <dd className="space-y-sm">
              <Button variant="plain" className="px-0 text-section font-semibold tabular-nums" aria-label={t('web.market.open_samples', { config: label(group) })} onClick={() => setSamples(group)}>{group.fair.price === null ? t('web.market.unavailable') : formatMoney(group.fair.price, locale)}</Button>
              <p className="text-caption text-text-secondary">{t('web.market.samples_count', { count: group.fair.sample })}</p>
              <p className="text-caption text-text-secondary">{mode !== 'keyword' && group.fair.price !== null ? messageText(group.fair.basis) : t('web.market.days', { days: group.fair.days, count: group.fair.days })}</p>
              {group.fair.dropped_spec !== null && <StatusBadge tone="warning">{t('web.market.dropped_spec', { spec: group.fair.dropped_spec })}</StatusBadge>}
            </dd>
          </div>
          <div className="min-w-0 space-y-sm">
            <dt className="text-caption text-text-secondary">{t('web.market.price_band')}</dt>
            <dd className="break-words tabular-nums">{group.price_band === null ? t('web.market.no_price_band') : `${formatStatisticMoney(group.price_band.p25, locale)} – ${formatStatisticMoney(group.price_band.p75, locale)}`}</dd>
          </div>
          {mode !== 'keyword' && <>
            <div className="min-w-0 space-y-sm"><dt className="text-caption text-text-secondary">{t('web.market.cheapest')}</dt><dd>{group.cheapest === null ? <p className="text-body text-text-secondary">{t('web.market.no_current_cheapest')}</p> : <Button variant="plain" className="tabular-nums" onClick={() => setListing(group.cheapest?.item_id)}>{formatMoney(group.cheapest.price, locale)}</Button>}</dd></div>
            <div className="min-w-0 space-y-sm"><dt className="text-caption text-text-secondary">{t('web.market.change_14_days')}</dt><dd><TrendValue trend={group.trend} /></dd></div>
          </>}
        </dl>
      </article>)}
    </div>}
    {current && <DailyPrices config={label(current)} points={current.daily} />}
    {providersPending && <StatePanel kind="pending" label={t('common.loading')} />}
    {providersError ? <InlineMessage tone="warning" action={<Button onClick={onRefresh}>{t('common.retry')}</Button>}>{errorText(providersError)}</InlineMessage> : null}
    <MarketInsightSection insight={data.insight} timezone={timezone} configured={aiConfigured} />
    {currentSamples && <MarketSamples key={`${taskId}:${identity(currentSamples)}`} taskId={taskId} group={currentSamples} config={label(currentSamples)} timezone={timezone} onClose={() => setSamples(undefined)} />}
    <Dialog open={listing !== undefined} onOpenChange={(open) => { if (!open) setListing(undefined); }} title={t('web.market.listing_details')} closeLabel={t('common.close')}>
      {listing !== undefined && <ListingPanel itemId={listing} />}
    </Dialog>
  </section>;
}

function TrendValue({ trend }: { trend: MarketGroup['trend'] }) {
  const { t, locale } = useTranslation();
  if (trend === null) return <p className="text-body text-text-secondary">{t('web.market.trend_unavailable')}</p>;
  return <div className="space-y-sm">
    <p className="tabular-nums">{t(trend.change > 0 ? 'web.market.increase' : trend.change < 0 ? 'web.market.decrease' : 'web.market.unchanged')} {formatStatisticMoney(Math.abs(trend.change), locale)}</p>
    <p className="text-body tabular-nums">{trend.percent === null ? t('web.market.percent_unavailable') : formatMarketPercent(trend.percent, locale)}</p>
    <p className="text-caption text-text-secondary">{t('web.market.trend_dates', { first_date: trend.first_day, last_date: trend.last_day })}</p>
  </div>;
}

export function MarketInsightSection({ insight, timezone, configured }: { insight: MarketInsight | null; timezone: string | undefined; configured?: boolean | undefined }) {
  const { t, locale } = useTranslation();
  const headingId = useId();
  if (configured !== true) return null;
  return <section aria-labelledby={headingId} className="min-w-0 space-y-md border-t border-border pt-xl">
    <h3 id={headingId} className="text-section font-semibold">{t('web.market.insight.title')}</h3>
    {insight ? <>
      {insight.generated_at !== null && <p className="text-caption text-text-secondary">{timezone ? t('web.market.insight.generated_at', { time: formatDateTime(insight.generated_at, locale, timezone) }) : t('web.market.timezone_unavailable')}</p>}
      <p className="max-w-prose whitespace-pre-wrap break-words text-body">{insight.text ?? t('web.market.insight.empty')}</p>
      {insight.model !== null && <p className="break-words text-caption text-text-secondary">{insight.model}</p>}
    </> : <StatePanel kind="empty" title={t('web.market.insight.empty')} description={t('web.market.insight.automatic')} />}
  </section>;
}
