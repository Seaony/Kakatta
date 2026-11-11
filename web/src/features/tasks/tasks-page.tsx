import { useEffect, useRef, useState, type FormEvent } from 'react';
import { Link } from '@tanstack/react-router';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiData, useApi, useApiError, useMessageText, useTimeZone } from '../../api/hooks';
import { ApiError } from '../../api/errors';
import { apiKey } from '../../api/keys';
import type { RequestQuery, Schema } from '../../api/types';
import type { TasksSearch } from '../../app/search';
import { useAuthentication } from '../../app/auth-state';
import { useTranslation } from '../../i18n';
import { formatDateTime, formatMoney } from '../../i18n/format';
import { Button, ConfirmDialog, InlineMessage, PageHeader, SelectField, Skeleton, StatePanel, StatusBadge, TextField } from '../../components/ui';
import { TaskEditor, TaskSaveFeedback, TaskTemplateButtons } from './task-editor';
import { TaskTransferDialog } from './task-transfer';
import { useTaskAccounts } from './queries';

export type TasksPageProps = { search?: TasksSearch; onSearchChange?(next: TasksSearch): void };
type Editor = { kind: 'new'; keyword?: string; template?: 'ipad' | 'macbook' | 'keyword' } | { kind: 'edit'; task: Schema<'Task'> } | { kind: 'copy'; task: Schema<'Task'> };
const phaseKeys = { idle: 'tasks.runtime.idle', queued: 'tasks.runtime.queued', running: 'tasks.runtime.running', finalizing: 'tasks.runtime.finalizing' } as const;

export function TasksPage({ search: suppliedSearch, onSearchChange }: TasksPageProps) {
  const api = useApi(), queryClient = useQueryClient(), errorText = useApiError();
  const { t } = useTranslation();
  const [localSearch, setLocalSearch] = useState<TasksSearch>({});
  const search = suppliedSearch ?? localSearch;
  const [searchText, setSearchText] = useState(search.q ?? '');
  const [editor, setEditor] = useState<Editor | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Schema<'Task'> | null>(null);
  const [transfer, setTransfer] = useState<'import' | 'export' | null>(null);
  const [saved, setSaved] = useState<Schema<'TaskSaveResult'> | null>(null);
  const titleRef = useRef<HTMLDivElement>(null);
  const focusedID = useRef<number | undefined>(undefined);
  const handledKeyword = useRef<string | undefined>(undefined);
  const composing = useRef(false);
  const updateSearch = (next: TasksSearch) => { if (onSearchChange) onSearchChange(next); else setLocalSearch(next); };
  const query: RequestQuery<'listTasks'> = { ...(search.q !== undefined ? { q: search.q } : {}), ...(search.enabled !== undefined ? { enabled: search.enabled } : {}), ...(search.limit !== undefined ? { limit: search.limit } : {}), ...(search.cursor ? { cursor: search.cursor } : {}) };
  const tasks = useQuery({ queryKey: apiKey('tasks', 'list', query), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/tasks', { params: { query }, signal })) });
  const focusedTask = useQuery({ queryKey: apiKey('tasks', 'detail', search.task_id), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/tasks/{task_id}', { params: { path: { task_id: search.task_id ?? 0 } }, signal })), enabled: search.task_id !== undefined });
  useEffect(() => { setSearchText(search.q ?? ''); }, [search.q]);
  useEffect(() => {
    if (search.create_keyword !== undefined && search.create_keyword !== handledKeyword.current) {
      handledKeyword.current = search.create_keyword;
      setEditor({ kind: 'new', keyword: search.create_keyword });
    }
    if (search.create_keyword === undefined) handledKeyword.current = undefined;
  }, [search.create_keyword]);
  useEffect(() => {
    if (focusedTask.data && search.task_id !== undefined && focusedID.current !== search.task_id) {
      focusedID.current = search.task_id;
      setEditor({ kind: 'edit', task: focusedTask.data });
    }
    if (search.task_id === undefined) focusedID.current = undefined;
  }, [focusedTask.data, search.task_id]);
  const closeEditor = () => { setTransfer(null); setEditor(null); setTimeout(() => { updateSearch({ ...search, task_id: undefined, create_keyword: undefined }); titleRef.current?.focus(); }, 0); };
  const remove = useMutation({ mutationFn: async (taskID: number) => { await api.DELETE('/api/v1/tasks/{task_id}', { params: { path: { task_id: taskID } } }); }, onSuccess() {
    setDeleteTarget(null);
    for (const resource of ['tasks', 'findings', 'overview', 'runs']) void queryClient.invalidateQueries({ queryKey: apiKey(resource) });
    queueMicrotask(() => titleRef.current?.focus());
  } });
  if (editor) {
    const isEdit = editor.kind === 'edit';
    return <div className="min-w-0 space-y-xl"><PageHeader title={t(isEdit ? 'tasks.edit' : editor.kind === 'copy' ? 'tasks.copy' : 'tasks.new')} description={t('tasks.editor.description')} />
      <TaskEditor key={editor.kind === 'new' ? `new-${editor.template ?? editor.keyword ?? ''}` : `${editor.kind}-${editor.task.id}`} {...(isEdit ? { initialTask: editor.task } : editor.kind === 'copy' ? { initialFields: { ...editor.task, account_id: editor.task.account_pinned ? editor.task.account_id : null } } : { ...(editor.keyword !== undefined ? { initialKeyword: editor.keyword } : {}), ...(editor.template ? { initialTemplate: editor.template } : {}) })} onCancel={closeEditor} onSaved={(result) => { setSaved(result); closeEditor(); }} />
    </div>;
  }
  const filtered = Boolean(search.q) || search.enabled !== undefined;
  const submitSearch = (event: FormEvent) => { event.preventDefault(); if (!composing.current) updateSearch({ ...search, q: searchText.trim() || undefined, cursor: undefined }); };
  return <div className="min-w-0 space-y-xl">
    <div ref={titleRef} tabIndex={-1}><PageHeader title={t('tasks.title')} description={t('tasks.description')} actions={<div className="flex flex-wrap gap-sm"><Button onClick={() => setTransfer('import')}>{t('tasks.import')}</Button><Button onClick={() => setTransfer('export')}>{t('tasks.export')}</Button><Button variant="primary" onClick={() => { setSaved(null); setEditor({ kind: 'new' }); }}>{t('tasks.new')}</Button></div>} /></div>
    {saved && <TaskSaveFeedback key={`${saved.task.id}-${saved.task.updated_at ?? ''}`} result={saved} />}
    <form onSubmit={submitSearch} className="flex min-w-0 flex-wrap items-end gap-md"><div className="min-w-0 basis-full sm:basis-[16rem] sm:grow"><TextField label={t('tasks.list.search')} value={searchText} onChange={(event) => setSearchText(event.target.value)} onCompositionStart={() => { composing.current = true; }} onCompositionEnd={() => { composing.current = false; }} onKeyDown={(event) => { if (event.key === 'Enter' && (event.nativeEvent.isComposing || composing.current)) event.preventDefault(); }} /></div><div className="min-w-0"><SelectField label={t('tasks.list.status')} value={search.enabled === undefined ? 'all' : search.enabled ? 'enabled' : 'paused'} options={[{ value: 'all', label: t('tasks.list.all') }, { value: 'enabled', label: t('tasks.list.enabled') }, { value: 'paused', label: t('tasks.list.paused') }]} onValueChange={(value) => updateSearch({ ...search, enabled: value === 'all' ? undefined : value === 'enabled', cursor: undefined })} /></div><Button type="submit">{t('tasks.list.search_action')}</Button><Button busy={tasks.isFetching} onClick={() => { void tasks.refetch(); }}>{t('tasks.refresh')}</Button></form>
    {search.task_id !== undefined && focusedTask.isPending && <p role="status" className="text-body text-text-secondary">{t('common.loading')}</p>}
    {focusedTask.error && <InlineMessage tone="danger" action={<Button onClick={() => { void focusedTask.refetch(); }}>{t('common.retry')}</Button>}>{errorText(focusedTask.error)}</InlineMessage>}
    {tasks.isPending && <StatePanel kind="pending" label={t('common.loading')}><Skeleton className="h-xxl" /><Skeleton className="h-xxl" /></StatePanel>}
    {tasks.error && !tasks.data && <StatePanel kind="error" title={t('tasks.list.load_failed')} description={errorText(tasks.error)} retry={{ label: t('common.retry'), run: () => { void tasks.refetch(); } }} />}
    {tasks.error && tasks.data && <InlineMessage tone="warning">{t('tasks.list.refresh_failed')} {errorText(tasks.error)}</InlineMessage>}
    {tasks.data && tasks.data.items.length === 0 && <StatePanel kind="empty" title={t(filtered ? 'tasks.list.empty_filtered' : 'tasks.empty.title')} description={t(filtered ? 'tasks.list.empty_filtered_description' : 'tasks.empty.description')} action={filtered ? <Button onClick={() => updateSearch({})}>{t('tasks.list.clear_filters')}</Button> : <div className="space-y-lg"><TaskTemplateButtons onSelect={(template) => setEditor({ kind: 'new', template })} /><Button variant="primary" onClick={() => setEditor({ kind: 'new' })}>{t('tasks.new')}</Button></div>} />}
    {tasks.data && tasks.data.items.length > 0 && <><p className="text-body text-text-secondary">{t('tasks.list.total', { count: tasks.data.total })}</p><div className="min-w-0 divide-y divide-border">{tasks.data.items.map((task) => <TaskRow key={task.id} task={task} onEdit={() => { setSaved(null); setEditor({ kind: 'edit', task }); updateSearch({ ...search, task_id: task.id }); }} onCopy={(current) => { setSaved(null); setEditor({ kind: 'copy', task: current }); }} onDelete={() => { remove.reset(); setDeleteTarget(task); }} />)}</div>
      <nav aria-label={t('tasks.list.pagination')} className="flex flex-wrap gap-md border-t border-border py-lg">{search.cursor && <Button onClick={() => updateSearch({ ...search, cursor: undefined })}>{t('tasks.list.first')}</Button>}<Button disabled={!tasks.data.next_cursor || tasks.isFetching} onClick={() => { if (tasks.data.next_cursor) updateSearch({ ...search, cursor: tasks.data.next_cursor }); }}>{t('tasks.list.next')}</Button></nav>
    </>}
    <ConfirmDialog open={deleteTarget !== null} onOpenChange={(open) => { if (!open && !remove.isPending) setDeleteTarget(null); }} title={t('tasks.delete.title')} description={t('tasks.delete.description')} confirmLabel={t('tasks.delete')} cancelLabel={t('common.cancel')} destructive pending={remove.isPending} {...(remove.error ? { error: errorText(remove.error) } : {})} onConfirm={() => { if (deleteTarget) remove.mutate(deleteTarget.id); }} />
    {transfer !== null && <TaskTransferDialog mode={transfer} onClose={() => setTransfer(null)} />}
  </div>;
}

function TaskRow({ task, onEdit, onCopy, onDelete }: { task: Schema<'TaskView'>; onEdit(): void; onCopy(task: Schema<'Task'>): void; onDelete(): void }) {
  const api = useApi(), queryClient = useQueryClient(), errorText = useApiError(), messageText = useMessageText();
  const authenticated = useAuthentication()?.authenticated !== false;
  const copyOperation = useRef<AbortController | null>(null);
  const [copyPending, setCopyPending] = useState(false);
  const [copyError, setCopyError] = useState<unknown>(null);
  const timeZone = useTimeZone();
  const accounts = useTaskAccounts();
  const { t, locale } = useTranslation();
  const run = useMutation({ mutationFn: async () => apiData(await api.POST('/api/v1/tasks/{task_id}/run', { params: { path: { task_id: task.id } } })), onSuccess() { for (const resource of ['tasks', 'runs', 'overview']) void queryClient.invalidateQueries({ queryKey: apiKey(resource) }); } });
  const toggle = useMutation({ mutationFn: async () => apiData(await api.PATCH('/api/v1/tasks/{task_id}', { params: { path: { task_id: task.id } }, body: { enabled: !task.enabled } })), onSuccess() { for (const resource of ['tasks', 'overview']) void queryClient.invalidateQueries({ queryKey: apiKey(resource) }); } });
  const dismiss = useMutation({ mutationFn: async () => { await api.POST('/api/v1/tasks/{task_id}/no-good-hint/dismiss', { params: { path: { task_id: task.id } } }); }, onSuccess() { void queryClient.invalidateQueries({ queryKey: apiKey('tasks') }); } });
  useEffect(() => () => { copyOperation.current?.abort(); copyOperation.current = null; }, []);
  useEffect(() => {
    if (!authenticated && copyOperation.current) { copyOperation.current.abort(); copyOperation.current = null; setCopyPending(false); }
  }, [authenticated]);
  const copy = async () => {
    if (copyOperation.current || !authenticated) return;
    const controller = new AbortController(); copyOperation.current = controller;
    setCopyPending(true); setCopyError(null);
    try {
      const current = apiData(await api.GET('/api/v1/tasks/{task_id}', { params: { path: { task_id: task.id } }, signal: controller.signal }));
      if (copyOperation.current === controller && !controller.signal.aborted) onCopy(current);
    } catch (error) { if (copyOperation.current === controller && !controller.signal.aborted) setCopyError(error); }
    finally { if (copyOperation.current === controller) { copyOperation.current = null; setCopyPending(false); } }
  };
  const reason = task.runtime.blocked?.reason ?? task.last_error;
  const failure = run.error ?? toggle.error ?? dismiss.error ?? copyError;
  const nextAllowed = failure instanceof ApiError ? failure.reason.args.next_allowed_at : null;
  const busy = run.isPending || toggle.isPending || dismiss.isPending || copyPending;
  const date = (value: number | null) => value === null ? t('tasks.runtime.never') : timeZone ? formatDateTime(value, locale, timeZone) : t('common.loading');
  return <article className="grid min-w-0 gap-lg py-xl lg:grid-cols-[minmax(0,1fr)_auto]" aria-labelledby={`task-${task.id}`}>
    <div className="min-w-0 space-y-md"><div className="flex flex-wrap items-center gap-md"><h2 id={`task-${task.id}`} className="text-section font-semibold">{task.name}</h2><StatusBadge tone={task.enabled ? 'positive' : 'neutral'}>{t(task.enabled ? 'tasks.enabled' : 'tasks.paused')}</StatusBadge><StatusBadge tone={task.runtime.phase === 'idle' ? 'neutral' : 'info'}>{t(phaseKeys[task.runtime.phase])}</StatusBadge><StatusBadge>{t(task.judge_mode === 'ai' ? 'tasks.mode.ai' : 'tasks.mode.keyword')}</StatusBadge></div>
      <p className="text-body text-text-body">{task.keyword}</p><div className="flex flex-wrap gap-x-lg gap-y-sm text-body text-text-secondary"><span>{t('tasks.runtime.interval', { count: task.interval_minutes })}</span>{(task.min_price !== null || task.max_price !== null) && <span>{t('tasks.runtime.price_range', { min: task.min_price === null ? t('tasks.range.unlimited') : formatMoney(task.min_price, locale), max: task.max_price === null ? t('tasks.range.unlimited') : formatMoney(task.max_price, locale) })}</span>}{task.region_province && <span>{[task.region_province, task.region_city, task.region_district].filter(Boolean).join(' / ')}</span>}</div>
      <div className="space-y-sm text-body text-text-secondary"><p>{t('tasks.runtime.account', { name: task.account_id === null ? t('tasks.account.unassigned') : accounts.data?.find((account) => account.id === task.account_id)?.name ?? t('tasks.account.unavailable', { id: task.account_id }) })} · {t(task.account_pinned ? 'tasks.account.pinned' : 'tasks.account.auto')}</p>{task.enabled && <p>{t('tasks.runtime.next', { time: date(task.next_run_at) })}</p>}<p>{t('tasks.runtime.last', { time: date(task.last_run_at) })}</p></div>
      {reason && <InlineMessage tone="warning">{messageText(reason)}{task.runtime.blocked?.next_allowed_at != null && timeZone && <p>{t('tasks.runtime.next_allowed', { time: formatDateTime(task.runtime.blocked.next_allowed_at, locale, timeZone) })}</p>}{task.account_id !== null && <Link to="/accounts" search={{ account_id: task.account_id }} className="inline-flex min-h-control items-center underline underline-offset-4">{t('tasks.runtime.account_action')}</Link>}</InlineMessage>}
      {run.data && <p role="status" className="text-body text-info">{t('tasks.runtime.accepted')}</p>}
      {failure !== null && failure !== undefined && <InlineMessage tone="danger"><p>{errorText(failure)}</p>{failure instanceof ApiError && failure.requestID && <p>{t('web.request_id', { request_id: failure.requestID })}</p>}{typeof nextAllowed === 'number' && Number.isSafeInteger(nextAllowed) && timeZone && <p>{t('tasks.runtime.next_allowed', { time: formatDateTime(nextAllowed, locale, timeZone) })}</p>}<Link to="/accounts" search={{ account_id: task.account_id ?? undefined }} className="inline-flex min-h-control items-center underline underline-offset-4">{t('tasks.runtime.account_action')}</Link></InlineMessage>}
      {task.no_good_hint && <InlineMessage tone="info" title={t('tasks.no_good.title', { count: task.no_good_hint.sample_count })} action={<div className="flex flex-wrap gap-sm"><Button onClick={onEdit}>{t('tasks.no_good.edit')}</Button><Button busy={dismiss.isPending} onClick={() => dismiss.mutate()}>{t('tasks.no_good.dismiss')}</Button></div>}>{task.no_good_hint.reasons.map((entry, index) => <p key={index}>{t('tasks.count_reason', { reason: messageText(entry.reason), count: entry.count })}</p>)}</InlineMessage>}
    </div>
    <div className="flex max-w-full flex-wrap content-start gap-sm lg:max-w-xs lg:justify-end"><Button disabled={busy || task.runtime.phase !== 'idle'} busy={run.isPending} onClick={() => run.mutate()}>{t('tasks.run_now')}</Button><Button disabled={busy} busy={toggle.isPending} onClick={() => toggle.mutate()}>{t(task.enabled ? 'tasks.pause' : 'tasks.enable')}</Button><Button disabled={busy} onClick={onEdit}>{t('tasks.edit')}</Button><Button disabled={busy || !authenticated} busy={copyPending} onClick={() => { void copy(); }}>{t('tasks.copy')}</Button>{copyPending && <Button onClick={() => { copyOperation.current?.abort(); copyOperation.current = null; setCopyPending(false); }}>{t('common.cancel')}</Button>}<Button variant="plain" disabled={busy} onClick={onDelete}>{t('tasks.delete')}</Button><Link to="/results" search={{ task_id: task.id }} className="inline-flex min-h-control items-center px-md text-body underline underline-offset-4">{t('tasks.results')}</Link><Link to="/logs" search={{ task_id: task.id }} className="inline-flex min-h-control items-center px-md text-body underline underline-offset-4">{t('tasks.logs')}</Link></div>
  </article>;
}
