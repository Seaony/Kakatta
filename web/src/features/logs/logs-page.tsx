import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import { keepPreviousData, useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { apiData, useApi, useMessageText } from '../../api/hooks';
import { ApiError } from '../../api/errors';
import { apiKey } from '../../api/keys';
import type { RequestQuery } from '../../api/types';
import type { LogsSearch } from '../../app/search';
import { Button, InlineMessage, PageHeader, Pagination, SelectField, Skeleton, StatePanel, TextField } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { StoredMessage } from '../../i18n/stored-message';
import { failureKinds, runDuration, useLogError, useLogFormat, utcInput, utcValue } from './display';
import { HourlyChart } from './hourly-chart';
import { RunDetail, RunOutcomeBadge } from './run-detail';
import { RunExportDialog } from './run-export';
import { SystemEvents } from './system-events';

export type LogsPageProps = { search?: LogsSearch; onSearchChange?(next: LogsSearch): void };

export function LogsPage({ search, onSearchChange }: LogsPageProps = {}) {
  const api = useApi();
  const queryClient = useQueryClient();
  const { t } = useTranslation();
  const errorText = useLogError();
  const [localSearch, setLocalSearch] = useState<LogsSearch>({});
  const current = search ?? localSearch;
  const changeSearch = (next: LogsSearch) => { if (onSearchChange) onSearchChange(next); else setLocalSearch(next); };
  const [expanded, setExpanded] = useState<ReadonlySet<number>>(() => new Set(current.run_id ? [current.run_id] : []));
  const [cursors, setCursors] = useState<(string | undefined)[]>([current.cursor]);
  const [exportScope, setExportScope] = useState<RequestQuery<'exportRuns'> | null>(null);
  const closeExport = useCallback(() => setExportScope(null), []);
  const settings = useQuery({ queryKey: apiKey('settings'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings', { signal })) });
  const overview = useQuery({ queryKey: apiKey('runs', 'overview'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/runs/overview', { signal })) });
  const timezoneValue = settings.data?.values['general.timezone'];
  const timezone = typeof timezoneValue === 'string' ? timezoneValue : null;
  const format = useLogFormat(timezone);
  const filters: RequestQuery<'exportRuns'> = {
    ...(current.task_id !== undefined ? { task_id: current.task_id } : {}),
    ...(current.outcome ? { outcome: current.outcome } : {}),
    ...(current.failure_kind ? { failure_kind: current.failure_kind } : {}),
    ...(current.q ? { q: current.q } : {}),
    ...(current.from_at !== undefined ? { from_at: current.from_at } : {}),
    ...(current.to_at !== undefined ? { to_at: current.to_at } : {}),
  };
  const query: RequestQuery<'listRuns'> = {
    ...filters,
    ...(current.cursor ? { cursor: current.cursor } : {}),
    limit: current.limit ?? 50,
  };
  const runs = useQuery({
    queryKey: apiKey('runs', 'list', query),
    queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/runs', { params: { query }, signal })),
    placeholderData: keepPreviousData,
  });
  useEffect(() => {
    if (current.run_id !== undefined) setExpanded((previous) => new Set([...previous, current.run_id!]));
  }, [current.run_id]);
  useEffect(() => { setCursors([undefined]); }, [current.task_id, current.outcome, current.failure_kind, current.q, current.from_at, current.to_at]);
  const page = Math.max(0, cursors.findIndex((cursor) => cursor === current.cursor)) + 1;
  const movePage = (target: number) => {
    const next = target > page ? runs.data?.next_cursor ?? undefined : cursors[target - 1];
    if (target > page && !next) return;
    if (target > page) setCursors((previous) => [...previous.slice(0, page), next]);
    const nextSearch = { ...current };
    if (next) nextSearch.cursor = next; else delete nextSearch.cursor;
    changeSearch(nextSearch);
  };
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: apiKey('runs') });
    void queryClient.invalidateQueries({ queryKey: apiKey('system-events') });
    void queryClient.invalidateQueries({ queryKey: apiKey('settings') });
  };
  const filtered = current.task_id !== undefined || Boolean(current.outcome || current.failure_kind || current.q || current.from_at !== undefined || current.to_at !== undefined);
  const rows = runs.data?.items ?? [];
  const stepRetention = settings.data?.values['retention.run_steps_days'];
  const detailDays = typeof stepRetention === 'number' ? stepRetention : null;
  const statsDays = settings.data?.values['retention.stats_days'];
  const retention = detailDays !== null && typeof statsDays === 'number'
    ? `${t('logs.retention.steps', { count: detailDays })} ${t('logs.retention.records', { count: statsDays })}` : null;
  return <div className="min-w-0 space-y-xxl text-text-primary">
    <PageHeader title={t('logs.title')} description={t('logs.description')} actions={<><Button busy={overview.isFetching || runs.isFetching} busyLabel={t('common.loading')} onClick={refresh}>{t('logs.refresh')}</Button><Button onClick={() => setExportScope(filters)}>{t('logs.export')}</Button></>} />
    <section className="space-y-xl" aria-labelledby="logs-summary-title">
      <div className="space-y-sm"><h2 id="logs-summary-title" className="text-section font-semibold">{t('logs.summary.window')}</h2><p className="text-body text-text-secondary">{t('logs.summary.global')}</p></div>
      {overview.isPending && <StatePanel kind="pending" label={t('common.loading')}><div className="grid grid-cols-2 gap-lg sm:grid-cols-4"><Skeleton /><Skeleton /><Skeleton /><Skeleton /></div></StatePanel>}
      {overview.isError && <InlineMessage tone="danger" action={<Button onClick={() => void overview.refetch()}>{t('common.retry')}</Button>}>{errorText(overview.error)}</InlineMessage>}
      {overview.data && <>
        <dl className="grid grid-cols-2 gap-x-xl gap-y-lg border-y border-border py-lg sm:grid-cols-4">
          {[
            [t('logs.summary.rate'), format.rate(overview.data.success_rate)],
            [t('logs.summary.total'), format.number(overview.data.total_count)],
            [t('logs.summary.failed'), format.number(overview.data.failed_count)],
            [t('logs.summary.duration'), format.duration(overview.data.average_duration_ms)],
          ].map(([label, value]) => <div key={label} className="min-w-0 space-y-sm"><dt className="text-body text-text-secondary">{label}</dt><dd className="break-words text-section font-semibold tabular-nums">{value}</dd></div>)}
        </dl>
        <div className="space-y-sm text-caption text-text-secondary"><p>{t('logs.summary.hint')}</p><p>{format.time(overview.data.from_at)} – {format.time(overview.data.to_at)}</p><p>{t('logs.summary.updated', { time: format.time(overview.data.as_of) })}</p></div>
        {overview.data.coverage.is_partial && <InlineMessage tone="warning">{t('logs.coverage_partial')}{overview.data.coverage.from_at !== null && <p className="mt-sm tabular-nums">{format.time(overview.data.coverage.from_at)} – {format.time(overview.data.coverage.to_at)}</p>}</InlineMessage>}
        <HourlyChart buckets={overview.data.buckets} timezone={timezone} />
      </>}
    </section>
    <section className="space-y-lg border-t border-border pt-xl" aria-labelledby="logs-list-title">
      <h2 id="logs-list-title" className="text-section font-semibold">{t('logs.filters')}</h2>
      {settings.isError && <InlineMessage tone="warning" action={<Button onClick={() => void settings.refetch()}>{t('common.retry')}</Button>}>{errorText(settings.error)}</InlineMessage>}
      <LogFilters search={current} onSearchChange={(next) => { setCursors([undefined]); changeSearch(next); }} timezone={timezone} error={runs.error} />
      {retention && <p className="text-caption text-text-secondary">{retention}</p>}
      {runs.isPending && <StatePanel kind="pending" label={t('common.loading')}><Skeleton /><Skeleton /><Skeleton /></StatePanel>}
      {runs.isError && <InlineMessage tone="danger" action={<Button onClick={() => void runs.refetch()}>{t('common.retry')}</Button>}>{errorText(runs.error)}</InlineMessage>}
      {runs.data && <div aria-busy={runs.isFetching} className="space-y-lg">
        <p role="status" className="text-body text-text-secondary">{t('logs.records', { count: runs.data.total })}{runs.isFetching ? ` · ${t('common.loading')}` : ''}</p>
        {rows.length === 0 && <StatePanel kind="empty" title={t(filtered ? 'logs.filtered_empty' : 'logs.empty')} action={filtered ? <Button onClick={() => changeSearch({})}>{t('logs.clear')}</Button> : <Link to="/tasks" className="inline-flex min-h-control items-center rounded-control px-md text-body underline underline-offset-4 focus-visible:outline focus-visible:outline-2 focus-visible:outline-focus">{t('logs.task')}</Link>} />}
        <ul className="divide-y divide-border">{rows.map((run) => {
          const open = expanded.has(run.id);
          return <li key={run.id} className="py-lg">
            <div className="flex flex-col items-start gap-md sm:flex-row sm:justify-between sm:gap-xl">
              <div className="min-w-0 space-y-sm"><div className="flex flex-wrap items-center gap-sm"><h3 className="break-words text-body font-medium">{run.task_name}</h3><RunOutcomeBadge run={run} /><span className="text-caption text-text-secondary">{t(`logs.trigger.${run.trigger}`)}</span></div><p className="break-words text-caption text-text-secondary tabular-nums">{format.time(run.started_at)} · {format.duration(runDuration(run))}</p>{run.ended_at !== null && run.message && <p className="break-words text-body text-text-body"><StoredMessage message={run.message} /></p>}{run.repeat_count > 1 && <p className="text-caption text-text-secondary">{t('logs.repeat', { count: run.repeat_count })}</p>}</div>
              <Button variant="plain" aria-expanded={open} aria-controls={`log-run-${run.id}`} onClick={() => setExpanded((previous) => { const next = new Set(previous); if (next.has(run.id)) next.delete(run.id); else next.add(run.id); return next; })}>{t(open ? 'logs.collapse' : 'logs.expand', { id: run.id })}</Button>
            </div>
            {open && <div id={`log-run-${run.id}`}><RunDetail runID={run.id} timezone={timezone} detailDays={detailDays} /></div>}
          </li>;
        })}</ul>
        {current.run_id !== undefined && !rows.some((run) => run.id === current.run_id) && <section className="border-t border-border pt-lg" aria-label={t('logs.expand', { id: current.run_id })}><RunDetail runID={current.run_id} timezone={timezone} detailDays={detailDays} /></section>}
        <Pagination page={page} hasNextPage={runs.data.next_cursor !== null} onPageChange={movePage} previousLabel={t('logs.previous')} nextLabel={t('logs.next')} pageLabel={t('logs.page', { page })} disabled={runs.isFetching} />
      </div>}
    </section>
    <SystemEvents timezone={timezone} onTaskLogs={(taskID) => { changeSearch({ task_id: taskID }); document.getElementById('logs-list-title')?.scrollIntoView({ block: 'start' }); }} />
    <RunExportDialog scope={exportScope} timezone={timezone} retention={retention} onClose={closeExport} />
  </div>;
}

function LogFilters({ search, onSearchChange, timezone, error }: { search: LogsSearch; onSearchChange(next: LogsSearch): void; timezone: string | null; error: unknown }) {
  const api = useApi();
  const { t } = useTranslation();
  const errorText = useLogError();
  const messageText = useMessageText();
  const fieldError = (key: string) => error instanceof ApiError ? error.fields.find((field) => field.key === key)?.reason : undefined;
  const fieldText = (key: string) => { const reason = fieldError(key); return reason ? messageText(reason) : undefined; };
  const [draft, setDraft] = useState(() => ({ task: String(search.task_id ?? ''), outcome: search.outcome ?? '', failure: search.failure_kind ?? '', q: search.q ?? '', from: utcInput(search.from_at), to: utcInput(search.to_at) }));
  const [invalid, setInvalid] = useState(false);
  const composing = useRef(false);
  const endInput = useRef<HTMLInputElement>(null);
  const tasks = useInfiniteQuery({
    queryKey: apiKey('tasks', 'log-filter'), initialPageParam: '',
    queryFn: async ({ pageParam, signal }) => apiData(await api.GET('/api/v1/tasks', { params: { query: { limit: 200, ...(pageParam ? { cursor: pageParam } : {}) } }, signal })),
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });
  useEffect(() => {
    setDraft({ task: String(search.task_id ?? ''), outcome: search.outcome ?? '', failure: search.failure_kind ?? '', q: search.q ?? '', from: utcInput(search.from_at), to: utcInput(search.to_at) });
    setInvalid(false);
  }, [search.task_id, search.outcome, search.failure_kind, search.q, search.from_at, search.to_at]);
  const taskRows = tasks.data?.pages.flatMap((page) => page.items) ?? [];
  const options = [{ value: '', label: t('logs.all_tasks') }, ...taskRows.map((task) => ({ value: String(task.id), label: task.name }))];
  if (draft.task && !options.some((option) => option.value === draft.task)) options.push({ value: draft.task, label: t('logs.task_number', { id: draft.task }) });
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (composing.current) return;
    const from = utcValue(draft.from);
    const to = utcValue(draft.to);
    if ((draft.from && from === undefined) || (draft.to && to === undefined) || (from !== undefined && to !== undefined && from >= to)) { setInvalid(true); endInput.current?.focus(); return; }
    const outcome = draft.outcome === 'success' || draft.outcome === 'failed' || draft.outcome === 'skipped' ? draft.outcome : undefined;
    const failure = failureKinds.find((kind) => kind === draft.failure);
    const taskID = draft.task ? Number(draft.task) : undefined;
    onSearchChange({
      ...(taskID !== undefined ? { task_id: taskID } : {}), ...(outcome ? { outcome } : {}),
      ...(failure ? { failure_kind: failure } : {}), ...(draft.q.trim() ? { q: draft.q.trim() } : {}),
      ...(from !== undefined ? { from_at: from } : {}), ...(to !== undefined ? { to_at: to } : {}),
    });
    setInvalid(false);
  };
  return <form onSubmit={submit} className="space-y-lg">
    <TextField label={t('logs.search')} hint={t('logs.search.hint')} value={draft.q} error={fieldText('q')} onChange={(event) => setDraft({ ...draft, q: event.currentTarget.value })} onCompositionStart={() => { composing.current = true; }} onCompositionEnd={() => { composing.current = false; }} onKeyDown={(event) => { if (event.key === 'Enter' && (composing.current || event.nativeEvent.isComposing)) event.preventDefault(); }} />
    <div className="grid min-w-0 gap-lg sm:grid-cols-2 lg:grid-cols-3">
      <div className="space-y-sm"><SelectField label={t('logs.task')} options={options} value={draft.task} error={fieldText('task_id')} onValueChange={(value) => setDraft({ ...draft, task: value })} />{tasks.hasNextPage && <Button variant="plain" busy={tasks.isFetchingNextPage} onClick={() => void tasks.fetchNextPage()}>{t('logs.tasks.more')}</Button>}</div>
      <SelectField label={t('logs.outcome')} options={[{ value: '', label: t('logs.all_outcomes') }, ...(['success', 'failed', 'skipped'] as const).map((value) => ({ value, label: t(`logs.outcome.${value}`) }))]} value={draft.outcome} error={fieldText('outcome')} onValueChange={(value) => setDraft({ ...draft, outcome: value })} />
      <SelectField label={t('logs.failure')} options={[{ value: '', label: t('logs.all_failures') }, ...failureKinds.map((value) => ({ value, label: t(`logs.failure.${value}`) }))]} value={draft.failure} error={fieldText('failure_kind')} onValueChange={(value) => setDraft({ ...draft, failure: value })} />
      <TextField type="datetime-local" step="any" label={t('logs.from')} value={draft.from} error={fieldText('from_at')} onChange={(event) => { setDraft({ ...draft, from: event.currentTarget.value }); setInvalid(false); }} />
      <TextField ref={endInput} type="datetime-local" step="any" label={t('logs.to')} value={draft.to} error={invalid ? t('logs.invalid_range') : fieldText('to_at')} onChange={(event) => { setDraft({ ...draft, to: event.currentTarget.value }); setInvalid(false); }} />
    </div>
    <p className="text-caption text-text-secondary">{t('logs.time_hint', { timezone: timezone ?? 'UTC' })}</p>
    {tasks.isError && <InlineMessage tone="warning" action={<Button onClick={() => void tasks.refetch()}>{t('common.retry')}</Button>}>{errorText(tasks.error)}</InlineMessage>}
    <div className="flex flex-wrap gap-sm"><Button type="submit">{t('logs.apply')}</Button><Button variant="plain" onClick={() => onSearchChange({})}>{t('logs.clear')}</Button></div>
  </form>;
}
