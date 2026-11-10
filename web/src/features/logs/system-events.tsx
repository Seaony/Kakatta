import { useInfiniteQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { useEffect, useRef } from 'react';
import { apiData, useApi } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import type { Schema } from '../../api/types';
import { Button, InlineMessage, Skeleton, StatePanel } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { StoredMessage } from '../../i18n/stored-message';
import { useLogError, useLogFormat } from './display';

export function SystemEvents({ timezone, onTaskLogs }: { timezone: string | null; onTaskLogs(taskID: number): void }) {
  const api = useApi();
  const { t } = useTranslation();
  const errorText = useLogError();
  const format = useLogFormat(timezone);
  const title = useRef<HTMLHeadingElement>(null);
  const focusedEvent = useRef<number | null>(null);
  const events = useInfiniteQuery({
    queryKey: apiKey('system-events', { resolved: false }),
    initialPageParam: '',
    queryFn: async ({ pageParam, signal }) => apiData(await api.GET('/api/v1/system/events', {
      params: { query: { resolved: false, limit: 50, ...(pageParam ? { cursor: pageParam } : {}) } }, signal,
    })),
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });
  const rows = events.data?.pages.flatMap((page) => page.items) ?? [];
  useEffect(() => {
    if (focusedEvent.current !== null && !rows.some((event) => event.id === focusedEvent.current) && document.activeElement === document.body) {
      title.current?.focus();
      focusedEvent.current = null;
    }
  }, [rows]);
  return <section className="space-y-lg border-t border-border pt-xl" aria-labelledby="logs-events-title">
    <h2 ref={title} tabIndex={-1} id="logs-events-title" className="text-section font-semibold">{t('logs.events.title')}</h2>
    {events.isPending && <StatePanel kind="pending" label={t('common.loading')}><Skeleton /></StatePanel>}
    {events.isError && <InlineMessage tone="danger" action={<Button onClick={() => void (events.isFetchNextPageError ? events.fetchNextPage() : events.refetch())}>{t('common.retry')}</Button>}>{errorText(events.error)}</InlineMessage>}
    {events.data && rows.length === 0 && <StatePanel kind="empty" title={t('logs.events.empty')} />}
    <ul className="divide-y divide-border">{rows.map((event) => <li key={event.id} onFocusCapture={() => { focusedEvent.current = event.id; }} className="flex flex-col items-start justify-between gap-md py-lg sm:flex-row sm:gap-xl">
      <div className="min-w-0 space-y-sm"><h3 className="text-body font-medium">{t(`logs.events.kind.${event.kind ?? 'unknown'}`)}</h3>{event.message && <p className="break-words text-body text-text-body"><StoredMessage message={event.message} /></p>}<p className="text-caption text-text-secondary tabular-nums">{format.time(event.created_at)}</p></div>
      <EventRecovery event={event} onTaskLogs={onTaskLogs} />
    </li>)}</ul>
    {events.hasNextPage && <Button busy={events.isFetchingNextPage} busyLabel={t('common.loading')} onClick={() => void events.fetchNextPage()}>{t('logs.events.more')}</Button>}
  </section>;
}

function EventRecovery({ event, onTaskLogs }: { event: Schema<'SystemEvent'>; onTaskLogs(taskID: number): void }) {
  const { t } = useTranslation();
  const linkClass = 'inline-flex min-h-control min-w-control shrink-0 items-center rounded-control px-md text-body text-text-label underline underline-offset-4 focus-visible:outline focus-visible:outline-2 focus-visible:outline-focus';
  if (event.kind === 'account_verify' || event.kind === 'account_login' || event.kind === 'account_deny') {
    return <Link to="/accounts" {...(event.subject_id !== null && event.subject_id > 0 ? { search: { account_id: event.subject_id } } : {})} className={linkClass}>{t('logs.events.account')}</Link>;
  }
  if (event.kind === 'ai_error' || event.kind === 'ai_budget') {
    return <Link to="/settings/ai" {...(event.kind === 'ai_error' && event.subject_id !== null && event.subject_id > 0 ? { search: { provider_id: event.subject_id } } : {})} className={linkClass}>{t('logs.events.ai')}</Link>;
  }
  if (event.kind === 'task_failure_pause' && event.subject_id !== null && event.subject_id > 0) {
    return <Button onClick={() => { if (event.subject_id !== null) onTaskLogs(event.subject_id); }}>{t('logs.events.task')}</Button>;
  }
  if (event.kind === 'login_failures') return <Link to="/settings/security" className={linkClass}>{t('logs.events.security')}</Link>;
  return <Link to="/settings/system" className={linkClass}>{t('logs.events.system')}</Link>;
}
