import { useState, type ReactNode } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { apiData, useApi, useApiError, useMessageText } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import { ApiError } from '../../api/errors';
import type { components, operations } from '../../api/schema.gen';
import { Button, InlineMessage, PageHeader, RadioGroup, Skeleton, StatePanel, StatusBadge } from '../../components/ui';
import { useTranslation, type TranslationKey } from '../../i18n';
import { formatDateTime, formatInteger, formatMoney } from '../../i18n/format';
import { ActivityChart, PriceTrend } from './charts';
import { decimal, percent, signedInteger } from './format';

type Overview = components['schemas']['OverviewView'];
type Task = components['schemas']['TaskOverview'];
type Event = components['schemas']['SystemEvent'];
type Comparison = components['schemas']['MetricComparison'];
type Period = NonNullable<NonNullable<operations['getOverview']['parameters']['query']>['period']>;

export type OverviewPageProps = { period?: Period; onPeriodChange?(period: Period): void };

const states = ['monitoring', 'checking', 'error', 'paused'] as const;
const stateLabels = {
  monitoring: 'overview.state.monitoring', checking: 'overview.state.checking',
  error: 'overview.state.error', paused: 'overview.state.paused',
} satisfies Record<Task['state'], TranslationKey>;
const stateTones = { monitoring: 'positive', checking: 'info', error: 'danger', paused: 'neutral' } as const;
const linkClass = 'inline-flex min-h-control items-center rounded-control py-sm text-body font-medium text-accent-text underline-offset-4 [@media(hover:hover)]:hover:underline focus-visible:underline';

export function OverviewPage({ period: suppliedPeriod, onPeriodChange }: OverviewPageProps) {
  const [localPeriod, setLocalPeriod] = useState<Period>('today');
  const period = suppliedPeriod ?? localPeriod;
  const api = useApi();
  const errorText = useApiError();
  const { t } = useTranslation();
  const query = useQuery({
    queryKey: apiKey('overview', period),
    queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/overview', { params: { query: { period } }, signal })),
  });
  function changePeriod(value: string) {
    if (value !== 'today' && value !== '7d' && value !== '30d') return;
    if (suppliedPeriod === undefined) setLocalPeriod(value);
    onPeriodChange?.(value);
  }
  return <div className="min-w-0 space-y-xl">
    <PageHeader title={t('overview.title')} actions={
      <Button busy={query.isFetching} busyLabel={t('overview.refreshing')} onClick={() => { void query.refetch(); }}>{t('overview.refresh')}</Button>
    }>
      <RadioGroup label={t('overview.period')} value={period} onValueChange={changePeriod} orientation="horizontal" options={[
        { value: 'today', label: t('overview.today') },
        { value: '7d', label: t('overview.week') },
        { value: '30d', label: t('overview.month') },
      ]} />
    </PageHeader>
    {query.isPending && <StatePanel kind="pending" label={t('common.loading')}>
      <div className="grid gap-lg sm:grid-cols-2 xl:grid-cols-4">{[0, 1, 2, 3].map((key) => <Skeleton key={key} className="h-[calc(var(--space-xxl)*3)]" />)}</div>
      <Skeleton className="mt-xl h-[calc(var(--space-xxl)*7)]" />
    </StatePanel>}
    {query.isError && !query.data && <div><StatePanel kind="error" title={t('overview.load_failed')} description={errorText(query.error)} retry={{ label: t('common.retry'), run: () => { void query.refetch(); } }} /><FailureDetails error={query.error} /></div>}
    {query.data && <>
      {query.isError && <InlineMessage tone="warning" title={t('overview.refresh_failed')} action={<Button busy={query.isFetching} onClick={() => { void query.refetch(); }}>{t('common.retry')}</Button>}><p>{errorText(query.error)}</p><FailureDetails error={query.error} /></InlineMessage>}
      {query.isFetching && <p role="status" className="text-body text-text-secondary">{t('overview.refreshing')}</p>}
      <OverviewContent data={query.data} />
    </>}
  </div>;
}

function FailureDetails({ error }: { error: unknown }) {
  const { t } = useTranslation();
  const messageText = useMessageText();
  if (!(error instanceof ApiError)) return null;
  return <div className="space-y-sm break-words text-caption">
    {error.fields.length > 0 && <ul>{error.fields.map((field, index) => <li key={`${field.key}:${index}`}>{messageText(field.reason)}</li>)}</ul>}
    {error.requestID && <p>{t('web.request_id', { request_id: error.requestID })}</p>}
  </div>;
}

function Section({ title, description, action, children, className = '' }: {
  title: string; description?: string; action?: ReactNode; children: ReactNode; className?: string;
}) {
  return <section className={`min-w-0 rounded-card border border-border bg-raised p-lg sm:p-xl ${className}`} aria-label={title}>
    <header className="mb-lg flex flex-wrap items-start justify-between gap-md">
      <div className="min-w-0"><h2 className="text-section font-semibold text-text-primary">{title}</h2>{description && <p className="mt-xs max-w-prose text-body text-text-secondary">{description}</p>}</div>
      {action}
    </header>
    {children}
  </section>;
}

function Stat({ label, value }: { label: string; value: ReactNode }) {
  return <div className="min-w-0"><dt className="text-caption text-text-secondary">{label}</dt><dd className="mt-xs break-words text-body font-semibold tabular-nums text-text-primary">{value}</dd></div>;
}

function Metric({ title, value, comparison, description, showComparison }: {
  title: string; value: number; comparison: Comparison | undefined; description?: string; showComparison: boolean;
}) {
  const { t, locale } = useTranslation();
  let change = t('overview.comparison_unavailable');
  if (comparison && comparison.previous_count < 50) change = t('overview.difference', { value: signedInteger(comparison.delta, locale) });
  else if (comparison?.ratio !== null && comparison?.ratio !== undefined) change = t('overview.difference', { value: percent(comparison.ratio, locale, true) });
  return <section className="min-w-0 rounded-card border border-border bg-raised p-lg sm:p-xl" aria-label={title}>
    <h2 className="text-body font-medium text-text-label">{title}</h2>
    <p className="mt-md break-words text-page font-semibold tabular-nums text-text-primary">{formatInteger(value, locale)}</p>
    {showComparison && <p className="mt-md text-caption text-text-secondary">{change}</p>}
    {description && <p className="mt-sm text-caption text-text-secondary">{description}</p>}
  </section>;
}

function OverviewContent({ data }: { data: Overview }) {
  const { t, locale } = useTranslation();
  const date = (value: number) => formatDateTime(value, locale, data.timezone);
  return <>
    <div className="space-y-sm break-words text-body text-text-secondary">
      <p>{t('overview.range', { from: date(data.from_at), to: date(data.to_at), timezone: data.timezone })}</p>
      <p>{t('overview.updated_at', { time: date(data.as_of) })}</p>
    </div>
    {(data.coverage.is_partial || data.coverage.from_at === null) && <InlineMessage tone="warning" title={t('overview.coverage_partial')}>
      {data.coverage.from_at === null ? t('overview.coverage_empty') : t('overview.coverage_range', { from: date(data.coverage.from_at), to: date(data.coverage.to_at) })}
    </InlineMessage>}
    <div className="grid gap-lg sm:grid-cols-2 xl:grid-cols-4">
      <Metric title={t('overview.scanned')} value={data.cards.scanned_count} comparison={data.comparison?.scanned} showComparison={data.period !== '30d' && data.comparison !== null} />
      <Metric title={t('overview.new')} value={data.cards.new_count} comparison={data.comparison?.new} description={t('overview.new_help')} showComparison={data.period !== '30d' && data.comparison !== null} />
      <Metric title={t('overview.recommended')} value={data.cards.recommended_count} comparison={data.comparison?.recommended} description={t('overview.recommendation_split', { ai: formatInteger(data.cards.recommended_ai_count, locale), keyword: formatInteger(data.cards.recommended_keyword_count, locale) })} showComparison={data.period !== '30d' && data.comparison !== null} />
      <Metric title={t('overview.price_drops')} value={data.cards.price_drop_count} comparison={data.comparison?.price_drop} showComparison={data.period !== '30d' && data.comparison !== null} />
    </div>
    {data.comparison && <p className="text-caption text-text-secondary">{t('overview.comparison', { from: date(data.comparison.from_at), to: date(data.comparison.to_at) })}</p>}
    {data.period !== '30d' && data.comparison === null && <p className="text-caption text-text-secondary">{t('overview.comparison_unavailable')}</p>}
    {data.tasks.length === 0 && <StatePanel kind="empty" title={t('overview.tasks_empty')} description={t('overview.tasks_empty_description')} action={<Link className={linkClass} to="/tasks">{t('overview.manage_tasks')}</Link>} />}
    <div className="grid items-start gap-lg xl:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
      <Section title={t('overview.activity')}><ActivityChart buckets={data.activity} timezone={data.timezone} asOf={data.as_of} /></Section>
      <TaskStatus tasks={data.tasks} timezone={data.timezone} />
    </div>
    <div className="grid items-start gap-lg xl:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
      <Section title={t('overview.task_performance')} description={t('overview.pick_rate_help')} action={<Link className={linkClass} to="/tasks">{t('overview.manage_tasks')}</Link>}>
        <TaskPerformance tasks={data.tasks} />
      </Section>
      <GradeDistribution grades={data.grade_distribution} />
    </div>
    <div className="grid items-start gap-lg md:grid-cols-2 xl:grid-cols-4">
      <Section title={t('overview.accounts')} description={t('overview.current_snapshot')} action={<Link className={linkClass} to="/accounts">{t('overview.open_settings')}</Link>}>
        <p className="text-body font-semibold tabular-nums">{t('overview.accounts_summary', { active: formatInteger(data.accounts.active, locale), total: formatInteger(data.accounts.total, locale) })}</p>
      </Section>
      <Section title={t('overview.ai')} action={<Link className={linkClass} to="/settings/ai">{t('overview.open_settings')}</Link>}>
        <dl className="grid grid-cols-2 gap-lg">
          <Stat label={t('overview.call_count')} value={formatInteger(data.ai.count, locale)} />
          <Stat label={t('overview.success_rate')} value={data.ai.success_rate === null ? t('overview.unknown') : percent(data.ai.success_rate, locale)} />
          <Stat label={t('overview.duration')} value={data.ai.average_duration_ms === null ? t('overview.unknown') : t('overview.seconds', { value: decimal(data.ai.average_duration_ms / 1000, locale) })} />
          <Stat label={t('overview.tokens')} value={formatInteger(data.ai.tokens, locale)} />
        </dl>
        <p className="mt-lg text-caption text-text-secondary">{t('web.ai.usage_incomplete')}</p>
      </Section>
      <Section title={t('overview.notifications')} action={<Link className={linkClass} to="/settings/notifications">{t('overview.open_settings')}</Link>}>
        <dl className="grid grid-cols-2 gap-lg">
          <Stat label={t('overview.delivery_count')} value={formatInteger(data.notifications.count, locale)} />
          <Stat label={t('overview.success_rate')} value={data.notifications.success_rate === null ? t('overview.unknown') : percent(data.notifications.success_rate, locale)} />
        </dl>
        <p className="mt-lg text-caption text-text-secondary">{t('overview.delivery_help')}</p>
      </Section>
      <Section title={t('overview.exceptions')}>
        <dl className="grid grid-cols-2 gap-lg">
          <Stat label={t('overview.risk_failures')} value={formatInteger(data.exceptions.risk_failure_count, locale)} />
          <Stat label={t('overview.network_failures')} value={formatInteger(data.exceptions.network_failure_count, locale)} />
        </dl>
        <h3 className="mb-sm mt-lg text-body font-semibold">{t('overview.unresolved')}</h3>
        {data.exceptions.unresolved_events.length === 0 ? <p className="text-body text-text-secondary">{t('overview.no_events')}</p> : <ul className="space-y-lg">{data.exceptions.unresolved_events.map((event) => <EventRow key={event.id} event={event} timezone={data.timezone} />)}</ul>}
      </Section>
    </div>
  </>;
}

function TaskStatus({ tasks, timezone }: { tasks: readonly Task[]; timezone: string }) {
  const { t, locale } = useTranslation();
  const messageText = useMessageText();
  return <Section title={t('overview.task_status')} description={t('overview.current_snapshot')}>
    <dl className="grid grid-cols-2 gap-lg">{states.map((state) => <Stat key={state} label={t(stateLabels[state])} value={formatInteger(tasks.filter((task) => task.state === state).length, locale)} />)}</dl>
    {tasks.length > 0 && <ul className="mt-lg divide-y divide-border">{tasks.map((task) => <li key={task.task_id} className="min-w-0 py-lg first:pt-0 last:pb-0">
      <div className="flex flex-wrap items-center justify-between gap-sm"><Link className={`${linkClass} break-words`} to="/logs" search={{ task_id: task.task_id }}>{task.name}</Link><StatusBadge tone={stateTones[task.state]}>{t(stateLabels[task.state])}</StatusBadge></div>
      {task.reason && <p className="mt-xs break-words text-body text-text-secondary">{messageText(task.reason)}</p>}
      <dl className="mt-md grid grid-cols-2 gap-lg">
        <Stat label={t('overview.success_rate')} value={task.success_rate === null ? t('overview.unknown') : percent(task.success_rate, locale)} />
        <Stat label={t('overview.duration')} value={task.average_duration_ms === null ? t('overview.unknown') : t('overview.seconds', { value: decimal(task.average_duration_ms / 1000, locale) })} />
        <div className="col-span-2"><Stat label={t('overview.next_check')} value={task.next_run_at === null ? t('overview.unknown') : formatDateTime(task.next_run_at, locale, timezone)} /></div>
      </dl>
    </li>)}</ul>}
    <p className="mt-lg text-caption text-text-secondary">{t('overview.schedule_help')}</p>
  </Section>;
}

function TaskPerformance({ tasks }: { tasks: readonly Task[] }) {
  const { t, locale } = useTranslation();
  return tasks.length === 0 ? <p className="text-body text-text-secondary">{t('overview.tasks_empty')}</p> : <ul className="divide-y divide-border">{tasks.map((task) => <li key={task.task_id} className="min-w-0 py-xl first:pt-0 last:pb-0">
    <div className="flex flex-wrap items-center justify-between gap-sm"><Link className={`${linkClass} min-w-0 break-words`} to="/results" search={{ task_id: task.task_id }}>{task.name}</Link><StatusBadge tone={stateTones[task.state]}>{t(stateLabels[task.state])}</StatusBadge></div>
    <dl className="my-lg grid grid-cols-2 gap-lg sm:grid-cols-4">
      <Stat label={t('overview.scanned')} value={formatInteger(task.scanned_count, locale)} />
      <Stat label={t('overview.recommended')} value={formatInteger(task.recommended_count, locale)} />
      <Stat label={t('overview.pick_rate')} value={task.recommendation_rate === null ? t('overview.unknown') : percent(task.recommendation_rate, locale)} />
      <Stat label={t('overview.lowest')} value={task.min_price === null ? t('overview.unknown') : formatMoney(task.min_price, locale)} />
    </dl>
    <h3 className="mb-md text-caption font-medium text-text-secondary">{t('overview.trend')}</h3>
    <PriceTrend days={task.trend} name={task.name} />
  </li>)}</ul>;
}

function GradeDistribution({ grades }: { grades: Overview['grade_distribution'] }) {
  const { t, locale } = useTranslation();
  const gradeLabels: Record<string, TranslationKey> = {
    steal: 'analysis.grade.steal', good: 'analysis.grade.good', fair: 'analysis.grade.fair',
    pricey: 'analysis.grade.pricey', avoid: 'analysis.grade.avoid', unpriced: 'overview.unpriced',
    unmatched: 'overview.unmatched', junk: 'overview.junk',
  };
  const gradeFills: Record<string, string> = {
    steal: 'bg-positive', good: 'bg-positive', fair: 'bg-text-label', pricey: 'bg-warning',
    avoid: 'bg-danger', unpriced: 'bg-info', unmatched: 'bg-text-secondary', junk: 'bg-text-secondary',
  };
  const maximum = Math.max(1, ...grades.map((grade) => grade.count));
  const anyResults = grades.some((grade) => grade.count > 0);
  return <Section title={t('overview.grade_distribution')} description={t('overview.grade_help')}>
    {!anyResults && <p className="mb-lg text-body text-text-secondary">{t('overview.grade_empty')}</p>}
    <ul className="space-y-lg">{grades.map((grade) => {
      const label = gradeLabels[grade.kind];
      return <li key={grade.kind}>
        <div className="mb-sm flex justify-between gap-lg text-body"><span className="min-w-0 break-words">{label ? t(label) : t('overview.unknown_grade', { kind: grade.kind })}</span><span className="font-semibold tabular-nums">{formatInteger(grade.count, locale)}</span></div>
        <div aria-hidden="true" className="h-xs bg-control"><div className={`h-full ${gradeFills[grade.kind] ?? 'bg-info'}`} style={{ width: `${grade.count / maximum * 100}%` }} /></div>
      </li>;
    })}</ul>
  </Section>;
}

function EventRow({ event, timezone }: { event: Event; timezone: string }) {
  const { t, locale } = useTranslation();
  const messageText = useMessageText();
  const subject = event.subject_id !== null && event.subject_id > 0 ? event.subject_id : undefined;
  const label = t('overview.handle');
  let target: ReactNode = null;
  switch (event.kind) {
    case 'account_verify': case 'account_login': case 'account_deny':
      target = <Link className={linkClass} to="/accounts" search={subject === undefined ? {} : { account_id: subject }}>{label}</Link>;
      break;
    case 'ai_error': case 'ai_budget':
      target = <Link className={linkClass} to="/settings/ai" search={event.kind === 'ai_error' && subject !== undefined ? { provider_id: subject } : {}}>{label}</Link>;
      break;
    case 'task_failure_pause':
      target = <Link className={linkClass} to="/logs" search={subject === undefined ? {} : { task_id: subject }}>{label}</Link>;
      break;
    case 'xianyu_layout': target = <Link className={linkClass} to="/logs">{label}</Link>; break;
    case 'login_failures': target = <Link className={linkClass} to="/settings/security">{label}</Link>; break;
  }
  return <li className="min-w-0 border-t border-border pt-md">
    <p className="break-words text-body">{event.message ? messageText(event.message) : t('overview.event_unknown')}</p>
    {event.created_at !== null && <p className="mt-xs break-words text-caption text-text-secondary"><time dateTime={new Date(event.created_at).toISOString()}>{formatDateTime(event.created_at, locale, timezone)}</time></p>}
    {target}
  </li>;
}
