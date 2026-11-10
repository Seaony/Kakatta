import { useInfiniteQuery, useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { apiData, useApi } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import type { Schema } from '../../api/types';
import { Button, InlineMessage, Skeleton, StatePanel, StatusBadge } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { StoredMessage } from '../../i18n/stored-message';
import { runDuration, useLogError, useLogFormat } from './display';

const metrics = [
  ['page_count', 'logs.metrics.pages'], ['scanned_count', 'logs.metrics.scanned'],
  ['new_count', 'logs.metrics.new'], ['layer1_rejected', 'logs.metrics.layer1'],
  ['layer2_count', 'logs.metrics.layer2'], ['layer2_rejected', 'logs.metrics.layer2_rejected'],
  ['detail_count', 'logs.metrics.details'], ['seller_page_count', 'logs.metrics.sellers'],
  ['analyzed_count', 'logs.metrics.analyzed'], ['recommended_count', 'logs.metrics.recommended'],
  ['notified_count', 'logs.metrics.notified'],
] as const;

export function RunOutcomeBadge({ run }: { run: Schema<'RunView'> }) {
  const { t } = useTranslation();
  const outcome = run.ended_at === null ? 'running' : run.outcome ?? 'unknown';
  const tone = outcome === 'success' ? 'positive' : outcome === 'failed' ? 'danger' : outcome === 'running' ? 'info' : 'neutral';
  return <StatusBadge tone={tone}>{t(`logs.outcome.${outcome}`)}</StatusBadge>;
}

export function RunDetail({ runID, timezone }: { runID: number; timezone: string | null }) {
  const api = useApi();
  const { t } = useTranslation();
  const errorText = useLogError();
  const format = useLogFormat(timezone);
  const detail = useQuery({
    queryKey: apiKey('runs', 'detail', runID),
    queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/runs/{run_id}', { params: { path: { run_id: runID } }, signal })),
  });
  if (detail.isPending) return <StatePanel kind="pending" label={t('common.loading')}><Skeleton /><Skeleton /></StatePanel>;
  if (!detail.data) return <StatePanel kind="error" title={t('web.error.title')} description={errorText(detail.error)} retry={{ label: t('common.retry'), run: () => void detail.refetch() }} />;
  const run = detail.data.run;
  return <div className="space-y-xl py-lg">
    {detail.isError && <InlineMessage tone="danger" action={<Button onClick={() => void detail.refetch()}>{t('common.retry')}</Button>}>{errorText(detail.error)}</InlineMessage>}
    <dl className="grid grid-cols-1 gap-lg text-body sm:grid-cols-2 lg:grid-cols-3">
      <div><dt className="text-text-secondary">{t('logs.account')}</dt><dd className="mt-xs break-words">{run.account_id === null ? run.account_name ?? t('logs.unknown') : <Link className="underline underline-offset-4" to="/accounts" search={{ account_id: run.account_id }}>{run.account_name ?? t('logs.unknown')}</Link>}</dd></div>
      <div><dt className="text-text-secondary">{t('logs.proxy')}</dt><dd className="mt-xs break-words">{run.proxy_name ?? t('logs.unknown')}</dd></div>
      <div><dt className="text-text-secondary">{t('logs.duration')}</dt><dd className="mt-xs tabular-nums">{format.duration(runDuration(run))}</dd></div>
      <div><dt className="text-text-secondary">{t('logs.started')}</dt><dd className="mt-xs break-words tabular-nums">{format.time(run.started_at)}</dd></div>
      <div><dt className="text-text-secondary">{t('logs.ended')}</dt><dd className="mt-xs break-words tabular-nums">{format.time(run.ended_at)}</dd></div>
      {run.failure_kind && run.ended_at !== null && <div><dt className="text-text-secondary">{t('logs.failure')}</dt><dd className="mt-xs">{t(`logs.failure.${run.failure_kind}`)}</dd></div>}
    </dl>
    {run.ended_at !== null && run.message && <p className="break-words text-body"><StoredMessage message={run.message} /></p>}
    {run.repeat_count > 1 && <p className="text-body text-text-secondary">{t('logs.repeat', { count: run.repeat_count })}</p>}
    <dl className="grid grid-cols-2 gap-x-xl gap-y-md text-body sm:grid-cols-3 lg:grid-cols-4">
      {metrics.map(([field, label]) => <div key={field} className="flex min-w-0 justify-between gap-md"><dt className="text-text-secondary">{t(label)}</dt><dd className="tabular-nums">{format.number(run[field])}</dd></div>)}
    </dl>
    {run.warnings.length > 0 && <section className="space-y-sm"><h4 className="text-body font-medium">{t('logs.warnings')}</h4><ul className="space-y-sm">{run.warnings.map((warning, index) => <li key={`${warning.code}:${index}`} className="break-words text-body text-warning"><StoredMessage message={warning} /></li>)}</ul></section>}
    <RunSteps runID={runID} initial={detail.data.steps} timezone={timezone} />
  </div>;
}

function RunSteps({ runID, initial, timezone }: { runID: number; initial: Schema<'PageRunStep'>; timezone: string | null }) {
  const api = useApi();
  const { t } = useTranslation();
  const format = useLogFormat(timezone);
  const errorText = useLogError();
  const steps = useInfiniteQuery({
    queryKey: apiKey('runs', 'steps', runID),
    initialPageParam: '',
    initialData: { pages: [initial], pageParams: [''] },
    staleTime: Infinity,
    queryFn: async ({ pageParam, signal }) => apiData(await api.GET('/api/v1/runs/{run_id}/steps', {
      params: { path: { run_id: runID }, query: { limit: 50, ...(pageParam ? { cursor: pageParam } : {}) } }, signal,
    })),
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });
  const rows = steps.data.pages.flatMap((page) => page.items);
  return <section className="space-y-md" aria-label={t('logs.steps')}>
    <h4 className="text-body font-medium">{t('logs.steps')}</h4>
    <p className="text-caption text-text-secondary">{t('logs.steps.limit')}</p>
    {rows.length === 0 && <p className="text-body text-text-secondary">{t('logs.steps.empty')}</p>}
    <ol className="divide-y divide-border">
      {rows.map((step) => <li key={step.seq} className="grid min-w-0 gap-sm py-md sm:grid-cols-[auto_auto_1fr] sm:gap-lg">
        <span className="text-caption text-text-secondary tabular-nums">{format.number(step.seq)} · {format.time(step.at)}</span>
        <div>{step.status === null ? <StatusBadge>{t('logs.unknown')}</StatusBadge> : <StatusBadge tone={step.status === 'fail' ? 'danger' : step.status === 'warn' ? 'warning' : 'positive'}>{t(`logs.step.${step.status}`)}</StatusBadge>}</div>
        <div className="min-w-0 space-y-xs break-words text-body"><p>{step.message ? <StoredMessage message={step.message} /> : t('logs.unknown')}</p>{step.detail && <p className="text-text-secondary"><StoredMessage message={step.detail} /></p>}</div>
      </li>)}
    </ol>
    {steps.isError && <InlineMessage tone="danger" action={<Button onClick={() => void (steps.isFetchNextPageError ? steps.fetchNextPage() : steps.refetch())}>{t('common.retry')}</Button>}>{errorText(steps.error)}</InlineMessage>}
    {steps.hasNextPage && <Button busy={steps.isFetchingNextPage} busyLabel={t('common.loading')} onClick={() => void steps.fetchNextPage()}>{t('logs.steps.more')}</Button>}
  </section>;
}
