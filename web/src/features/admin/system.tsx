import { Link } from '@tanstack/react-router';
import { useInfiniteQuery, useQuery } from '@tanstack/react-query';
import { ApiError } from '../../api/errors';
import { apiData, useApi, useMessageText } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import type { components } from '../../api/schema.gen';
import { Button, FormSection, InlineMessage, PageHeader, StatePanel, StatusBadge } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { formatInteger } from '../../i18n/format';
import { ErrorNotice, useAdminTime } from './shared';
import { DiagnosticsExport } from './diagnostics';

type Session = components['schemas']['SessionView'];

function SessionLabels({ session }: { session: Session }) {
  const { t } = useTranslation();
  const kinds = { run: 'admin.session.kind.run', login: 'admin.session.kind.login', verification: 'admin.session.kind.verification' } as const;
  const phases = { queued: 'admin.session.phase.queued', opening: 'admin.session.phase.opening', running: 'admin.session.phase.running', waiting_login: 'admin.session.phase.waiting_login', verify_hold: 'admin.session.phase.verify_hold', verifying: 'admin.session.phase.verifying', closing: 'admin.session.phase.closing' } as const;
  return <div className="flex flex-wrap gap-sm"><StatusBadge>{t(kinds[session.kind])}</StatusBadge><StatusBadge tone="info">{t(phases[session.phase])}</StatusBadge></div>;
}

function Quota({ label, used, limit }: { label: string; used: number; limit: number }) {
  const { locale } = useTranslation();
  return <div className="min-w-0 space-y-xs"><dt className="text-body text-text-secondary">{label}</dt><dd className="text-body tabular-nums">{formatInteger(used, locale)} / {formatInteger(limit, locale)}</dd><progress className="h-sm w-full accent-accent-text" max={Math.max(1, limit)} value={Math.min(used, Math.max(1, limit))} aria-label={label} /></div>;
}

export function SystemPage() {
  const { t, locale } = useTranslation();
  const api = useApi();
  const message = useMessageText();
  const time = useAdminTime();
  const status = useQuery({ queryKey: apiKey('system-status'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/system/status', { signal })) });
  const storageStopped = status.error instanceof ApiError && ['runtime.storage_full', 'runtime.storage_locked', 'runtime.storage_unavailable'].includes(status.error.reason.code);
  const canReadUpdates = status.isSuccess && !storageStopped;
  const update = useQuery({ queryKey: apiKey('update-status'), enabled: canReadUpdates, queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/system/update', { signal })) });
  const definitions = useQuery({ queryKey: apiKey('setting-definitions'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings/definitions', { signal })) });
  const health = useQuery({ queryKey: apiKey('system-status', 'health'), queryFn: async ({ signal }) => {
    try { return apiData(await api.GET('/healthz', { signal })).status === 'ok'; }
    catch (error) { if (error instanceof ApiError && error.status === 503) return false; throw error; }
  } });
  const events = useInfiniteQuery({
    queryKey: apiKey('system-events', { resolved: false }), initialPageParam: null as string | null,
    queryFn: async ({ signal, pageParam }) => apiData(await api.GET('/api/v1/system/events', { signal, params: { query: { resolved: false, limit: 50, ...(pageParam ? { cursor: pageParam } : {}) } } })),
    getNextPageParam: (page) => page.next_cursor ?? undefined,
  });
  const rows = events.data?.pages.flatMap((page) => page.items) ?? [];
  const bytes = (value: number | null) => value === null ? t('admin.unknown') : new Intl.NumberFormat(locale, { style: 'unit', unit: 'megabyte', maximumFractionDigits: 1 }).format(value / (1024 * 1024));
  const refresh = () => {
    void health.refetch(); void events.refetch();
    void status.refetch().then((current) => {
      // 恢复时由查询重新启用后读取，避免重复请求。
      if (current.isSuccess && canReadUpdates) void update.refetch();
    });
  };
  return <div className="min-w-0 space-y-xxl">
    <PageHeader title={t('admin.system.title')} description={t('admin.system.description')} actions={<><DiagnosticsExport /><Button onClick={refresh} busy={status.isFetching || health.isFetching || events.isFetching || update.isFetching}>{t('admin.refresh')}</Button></>} />
    <FormSection title={t('admin.system.health')}>
      {health.isPending ? <StatePanel kind="pending" label={t('common.loading')} /> : health.isError ? <ErrorNotice error={health.error} /> : <StatusBadge tone={health.data ? 'positive' : 'danger'}>{t(health.data ? 'admin.system.health_ok' : 'admin.system.health_unavailable')}</StatusBadge>}
      {!storageStopped && <><ErrorNotice error={update.error} />{update.data?.error && <InlineMessage tone="warning"><p>{message(update.data.error)}</p><Link to="/settings/about" className="inline-flex min-h-control items-center text-accent-text underline">{t('admin.about.updates')}</Link></InlineMessage>}</>}
    </FormSection>
    {status.isError && <><ErrorNotice error={status.error} />{status.data && <InlineMessage tone="warning">{t('admin.stale')}</InlineMessage>}<Button onClick={() => void status.refetch()}>{t('common.retry')}</Button></>}
    {status.isPending && <StatePanel kind="pending" label={t('common.loading')} />}
    {status.data && !storageStopped && <>
      <p className="text-caption text-text-secondary">{t('admin.system.as_of', { time: time(status.data.as_of) })}</p>
      <div className="grid min-w-0 gap-xxl lg:grid-cols-2">
        <FormSection title={t('admin.system.scheduler')}>
          <div className="flex flex-wrap gap-sm"><StatusBadge tone={status.data.scheduler.running ? 'positive' : 'warning'}>{t(status.data.scheduler.running ? 'admin.system.running' : 'admin.system.stopped')}</StatusBadge><StatusBadge tone={status.data.scheduler.accepting ? 'neutral' : 'warning'}>{t(status.data.scheduler.accepting ? 'admin.system.accepting' : 'admin.system.not_accepting')}</StatusBadge></div>
          {status.data.scheduler.blocked && <InlineMessage tone="warning"><p>{message(status.data.scheduler.blocked.reason)}</p>{status.data.scheduler.blocked.next_allowed_at !== null && <p>{t('admin.system.blocked_until', { time: time(status.data.scheduler.blocked.next_allowed_at) })}</p>}</InlineMessage>}
          <dl className="space-y-md text-body"><div className="flex flex-wrap justify-between gap-md"><dt>{t('admin.system.queued')}</dt><dd className="tabular-nums">{formatInteger(status.data.scheduler.queued_count, locale)}</dd></div><div className="flex flex-wrap justify-between gap-md"><dt>{t('admin.system.running_count')}</dt><dd className="tabular-nums">{formatInteger(status.data.scheduler.running_count, locale)}</dd></div><div><dt className="text-text-secondary">{t('admin.system.next_wake')}</dt><dd className="break-words tabular-nums">{time(status.data.scheduler.next_wake_at)}</dd></div></dl>
        </FormSection>
        <FormSection title={t('admin.system.browsers')}>
          <p className="text-body tabular-nums">{t('admin.system.used_of', { count: status.data.browsers.limit, used: status.data.browsers.active, limit: status.data.browsers.limit })}</p>
          {status.data.browsers.sessions.length ? <ul className="space-y-md">{status.data.browsers.sessions.map((session) => <li key={session.id} className="space-y-sm"><Link className="inline-flex min-h-control items-center text-body text-accent-text underline" to="/accounts" search={{ account_id: session.account_id }}>{t('admin.system.account', { id: session.account_id })}</Link><SessionLabels session={session} /></li>)}</ul> : <p className="text-body text-text-secondary">{t('admin.system.browser_idle')}</p>}
        </FormSection>
      </div>
      <FormSection title={t('admin.system.configuration')} description={t('admin.system.configuration_help')}>
        {!status.data.configuration.length ? <StatePanel kind="empty" title={t('admin.system.no_checks')} /> : <ul className="divide-y divide-border">{status.data.configuration.map((check) => {
          const definition = definitions.data?.find((entry) => entry.key === check.key);
          return <li key={check.key} className="flex min-w-0 flex-wrap items-start justify-between gap-md py-md"><div className="min-w-0 space-y-xs"><p className="break-words text-body">{definition ? message(definition.label) : check.key}</p>{check.reason && <p className="max-w-prose break-words text-body text-warning">{message(check.reason)}</p>}</div><StatusBadge tone={!check.ok ? 'danger' : check.reason ? 'warning' : 'positive'}>{t(check.ok && !check.reason ? 'admin.system.check_passed' : 'admin.system.check_failed')}</StatusBadge></li>;
        })}</ul>}
      </FormSection>
      <FormSection title={t('admin.system.quotas')} description={t('admin.system.quota_help')}>
        {status.data.quotas.length ? <ul className="divide-y divide-border">{status.data.quotas.map((account) => <li key={account.account_id} className="space-y-md py-lg first:pt-0 last:pb-0"><Link to="/accounts" search={{ account_id: account.account_id }} className="inline-flex min-h-control items-center break-words text-body font-medium text-accent-text underline">{account.name}</Link><dl className="grid gap-lg sm:grid-cols-3"><Quota label={t('admin.system.hourly')} used={account.quota.hourly_used} limit={account.quota.hourly_limit} /><Quota label={t('admin.system.daily_search')} used={account.quota.daily_search_used} limit={account.quota.daily_search_limit} /><Quota label={t('admin.system.daily_detail')} used={account.quota.daily_detail_used} limit={account.quota.daily_detail_limit} /></dl><p className="break-words text-caption text-text-secondary">{t('admin.system.window_start')}: {time(account.quota.day_start_at)}</p></li>)}</ul> : <StatePanel kind="empty" title={t('admin.system.empty_quotas')} action={<Link to="/accounts" className="inline-flex min-h-control items-center text-accent-text underline">{t('admin.system.accounts')}</Link>} />}
      </FormSection>
      <FormSection title={t('admin.system.resources')} description={t('admin.system.resource_scope')}>
        <dl className="grid gap-lg text-body sm:grid-cols-2 lg:grid-cols-4">{([
          ['admin.system.disk_free', status.data.resources.disk_free_bytes], ['admin.system.disk_total', status.data.resources.disk_total_bytes], ['admin.system.memory_used', status.data.resources.memory_used_bytes], ['admin.system.memory_total', status.data.resources.memory_total_bytes],
        ] as const).map(([key, value]) => <div key={key}><dt className="text-text-secondary">{t(key)}</dt><dd className="tabular-nums">{bytes(value)}</dd></div>)}</dl>
      </FormSection>
    </>}
    {!storageStopped && <FormSection title={t('admin.system.events')}>
      {events.isPending ? <StatePanel kind="pending" label={t('common.loading')} /> : rows.length ? <ul className="divide-y divide-border">{rows.map((event) => <li key={event.id} className="space-y-sm py-md"><p className="max-w-prose break-words text-body">{event.message ? message(event.message) : t('admin.unknown')}</p><p className="text-caption text-text-secondary">{time(event.created_at)}</p>{event.kind === 'login_failures' && <Link to="/settings/security" className="inline-flex min-h-control items-center text-accent-text underline">{t('admin.security.title')}</Link>}{event.subject_id !== null && event.subject_id > 0 && (event.kind === 'account_verify' || event.kind === 'account_deny' || event.kind === 'account_login') && <Link to="/accounts" search={{ account_id: event.subject_id }} className="inline-flex min-h-control items-center text-accent-text underline">{t('admin.system.accounts')}</Link>}{event.kind === 'ai_error' || event.kind === 'ai_budget' ? <Link to="/settings/ai" className="inline-flex min-h-control items-center text-accent-text underline">{t('admin.system.ai')}</Link> : null}{event.kind === 'task_failure_pause' && event.subject_id !== null && <Link to="/logs" search={{ task_id: event.subject_id }} className="inline-flex min-h-control items-center text-accent-text underline">{t('admin.system.logs')}</Link>}</li>)}</ul> : !events.isError && <StatePanel kind="empty" title={t('admin.system.empty_events')} />}
      <ErrorNotice error={events.error} />{events.hasNextPage && <Button onClick={() => void events.fetchNextPage()} busy={events.isFetchingNextPage}>{t('admin.more')}</Button>}
    </FormSection>}
  </div>;
}
