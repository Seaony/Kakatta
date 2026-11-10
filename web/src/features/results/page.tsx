import { useEffect, useRef, useState } from 'react';
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { apiData, useApi, useApiError, useMessageText } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import { ApiError, isCancellation } from '../../api/errors';
import type { ResultsSearch } from '../../app/search';
import type { Schema } from '../../api/types';
import type { Message } from '../../i18n';
import { useTranslation } from '../../i18n';
import { StoredMessage } from '../../i18n/stored-message';
import { Button, ConfirmDialog, Dialog, InlineMessage, PageHeader, Pagination, Skeleton, StatePanel } from '../../components/ui';
import { FindingDetail } from '../detail-compare';
import { MarketPanel } from '../market';
import { FindingCard, type FindingAction } from './finding-card';
import { FindingsCSVExport } from './csv-export';
import { ResultsFilters } from './filters';
import { ListingPanel, SavedListingCard } from './listing-panel';
import { detailQuery, findingQuery, findingsExportQuery, listingQuery, resultTime, selectableIDs, type Finding } from './model';
import { previewReanalysisBatches, queueReanalysisBatches } from './reanalysis-batches';

type Confirmation =
  | { kind: 'delete'; ids: number[] }
  | { kind: 'delete-all'; taskId: number }
  | { kind: 'dismiss'; ids: number[] }
  | { kind: 'block'; sellerId: string; name: string }
  | { kind: 'reanalyze'; ids: number[]; single: boolean; allFailed?: boolean; prepared?: Schema<'ReanalysisPreview'> };

type Operation = Confirmation
  | { kind: 'favorite'; finding: Finding }
  | { kind: 'restore'; finding: Finding }
  | { kind: 'unread'; finding: Finding }
  | { kind: 'run'; taskId: number }
  | { kind: 'dismiss-hint'; taskId: number };

const accountReasons = new Set(['run.no_account', 'run.account_signed_out', 'run.account_expired', 'run.account_needs_verification', 'run.account_denied', 'run.account_cooling', 'run.account_gap']);
const aiReasons = new Set(['run.no_ai_provider', 'run.ai_daily_limit']);

export type ResultsPageProps = {
  search: ResultsSearch;
  onSearchChange(next: ResultsSearch): void;
};

export function ResultsPage({ search, onSearchChange }: ResultsPageProps) {
  const api = useApi();
  const queryClient = useQueryClient();
  const { t, locale } = useTranslation();
  const errorText = useApiError();
  const message = useMessageText();
  const [selected, setSelected] = useState<number[]>([]);
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null);
  const [feedback, setFeedback] = useState<Message | null>(null);
  const [skippedItems, setSkippedItems] = useState<Schema<'BatchItem'>[]>([]);
  const [reanalysisItems, setReanalysisItems] = useState<Schema<'ReanalysisItem'>[]>([]);
  const [selectionError, setSelectionError] = useState<unknown>(null);
  const [selecting, setSelecting] = useState(false);
  const [allProgress, setAllProgress] = useState<Message | null>(null);
  const [openItem, setOpenItem] = useState<string | null>(null);
  const [cursorTrail, setCursorTrail] = useState<Array<string | undefined>>([search.cursor]);
  const selectingController = useRef<AbortController | null>(null);
  const reanalysisController = useRef<AbortController | null>(null);
  const mounted = useRef(false);
  const resultsHeading = useRef<HTMLDivElement>(null);
  const filtersIdentity = JSON.stringify([search.task_id, search.collection, search.view, search.decision, search.price_drop, search.over_budget, search.q, search.sort, search.limit]);
  const activeView = useRef(filtersIdentity);
  const mutationView = useRef(filtersIdentity);
  // 筛选切走又切回来，旧操作仍属于上一轮。
  const viewVersion = useRef(0);
  const mutationVersion = useRef(0);
  if (activeView.current !== filtersIdentity) { activeView.current = filtersIdentity; viewVersion.current++; }
  useEffect(() => {
    setSelected([]);
    setConfirmation(null);
    setFeedback(null);
    setSkippedItems([]);
    setReanalysisItems([]);
    setAllProgress(null);
    setCursorTrail([search.cursor]);
    setSelectionError(null);
    selectingController.current?.abort();
    reanalysisController.current?.abort();
    setSelecting(false);
  }, [filtersIdentity]);
  useEffect(() => {
    mounted.current = true;
    const stop = () => {
      selectingController.current?.abort();
      reanalysisController.current?.abort();
    };
    const unsubscribe = api.subscribeAuthenticationLost(() => { stop(); setConfirmation(null); setSelecting(false); });
    return () => { mounted.current = false; stop(); unsubscribe(); };
  }, [api]);

  const isList = search.finding_id === undefined;
  const tasks = useInfiniteQuery({
    queryKey: apiKey('tasks', 'result-options'), initialPageParam: '', enabled: isList,
    queryFn: async ({ pageParam, signal }) => apiData(await api.GET('/api/v1/tasks', { params: { query: { limit: 200, ...(pageParam ? { cursor: pageParam } : {}) } }, signal })),
    getNextPageParam: (page) => page.next_cursor ?? undefined,
  });
  const taskId = search.task_id;
  const csvQuery = findingsExportQuery(search);
  const task = useQuery({
    queryKey: apiKey('tasks', taskId), enabled: taskId !== undefined && isList,
    queryFn: async ({ signal }) => {
      if (taskId === undefined) throw new ApiError(0, { code: 'web.invalid_response', args: {} });
      return apiData(await api.GET('/api/v1/tasks/{task_id}', { params: { path: { task_id: taskId } }, signal }));
    },
  });
  const settings = useQuery({ queryKey: apiKey('settings'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings', { signal })), enabled: isList });
  const zoneValue = settings.data?.values['general.timezone'];
  const timeZone = typeof zoneValue === 'string' ? zoneValue : undefined;
  const query = findingQuery(search);
  const findings = useQuery({
    queryKey: apiKey('findings', 'list', query), enabled: search.collection === undefined && isList,
    queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/findings', { params: { query }, signal })),
  });
  const savedQuery = listingQuery(search);
  const listings = useQuery({
    queryKey: apiKey('listings', 'list', savedQuery), enabled: search.collection !== undefined && isList,
    queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/listings', { params: { query: savedQuery }, signal })),
  });

  const refreshResults = async () => {
    await Promise.all(['findings', 'listings', 'tasks', 'overview', 'blocked-sellers'].map((resource) => queryClient.invalidateQueries({ queryKey: apiKey(resource) })));
  };
  const mutation = useMutation({
    mutationFn: async (operation: Operation): Promise<Message | Schema<'BatchResult'> | Schema<'ReanalysisResult'> | null> => {
      if (operation.kind === 'favorite' || operation.kind === 'restore') {
        const finding = operation.finding;
        apiData(await api.PATCH('/api/v1/listings/{item_id}', {
          params: { path: { item_id: finding.item_id } },
          body: operation.kind === 'favorite' ? { is_favorite: !finding.listing.is_favorite } : { is_dismissed: false },
        }));
        return { code: 'results.action.changed', args: {} };
      }
      if (operation.kind === 'unread') {
        apiData(await api.PATCH('/api/v1/findings/{finding_id}', { params: { path: { finding_id: operation.finding.id } }, body: { is_read: false } }));
        return { code: 'results.action.changed', args: {} };
      }
      if (operation.kind === 'run') {
        const result = apiData(await api.POST('/api/v1/tasks/{task_id}/run', { params: { path: { task_id: operation.taskId } } }));
        return { code: result.status === 'queued' ? 'results.run.queued' : 'results.run.started', args: {} };
      }
      if (operation.kind === 'dismiss-hint') {
        await api.POST('/api/v1/tasks/{task_id}/no-good-hint/dismiss', { params: { path: { task_id: operation.taskId } } });
        return null;
      }
      if (operation.kind === 'block') {
        apiData(await api.PUT('/api/v1/blocked-sellers/{seller_id}', { params: { path: { seller_id: operation.sellerId } }, body: { nick: operation.name } }));
        return { code: 'results.action.changed', args: {} };
      }
      if (operation.kind === 'delete-all') {
        const result = apiData(await api.DELETE('/api/v1/tasks/{task_id}/findings', { params: { path: { task_id: operation.taskId } } }));
        return { code: 'results.action.applied', args: { count: result.applied } };
      }
      if (operation.kind === 'delete' && operation.ids.length === 1) {
        const id = operation.ids[0];
        if (id === undefined) throw new ApiError(0, { code: 'web.invalid_response', args: {} });
        await api.DELETE('/api/v1/findings/{finding_id}', { params: { path: { finding_id: id } } });
        return { code: 'results.action.applied', args: { count: 1 } };
      }
      if (operation.kind === 'reanalyze' && operation.single) {
        const id = operation.ids[0];
        if (id === undefined) throw new ApiError(0, { code: 'web.invalid_response', args: {} });
        const result = apiData(await api.POST('/api/v1/findings/{finding_id}/reanalyze', { params: { path: { finding_id: id } } }));
        return result;
      }
      if (operation.kind === 'reanalyze' && operation.allFailed) {
        const controller = new AbortController();
        reanalysisController.current = controller;
        const origin = mutationView.current;
        const version = mutationVersion.current;
        try {
          return await queueReanalysisBatches(api, operation.ids, controller.signal, (progress) => {
            if (!mounted.current || activeView.current !== origin || viewVersion.current !== version) return;
            const items = [...progress.items];
            const queued = items.filter((item) => item.status === 'queued').length;
            setReanalysisItems(items);
            setFeedback(queued > 0 ? { code: 'results.action.queued', args: { count: queued } } : null);
            setAllProgress(progress.stopped
              ? { code: 'results.reanalyze.stopped', args: { confirmed: items.length, queued, uncertain: progress.uncertain, remaining: progress.remaining } }
              : { code: 'results.reanalyze.progress', args: { confirmed: items.length, total: progress.total, queued } });
          });
        } finally {
          if (reanalysisController.current === controller) reanalysisController.current = null;
        }
      }
      const result = apiData(await api.POST('/api/v1/findings/batch', {
        body: { ids: operation.ids, action: operation.kind === 'reanalyze' ? 'reanalyze' : operation.kind === 'dismiss' ? 'dismiss' : 'delete' },
      }));
      return result;
    },
    onSuccess: async (result, operation) => {
      const sameView = mounted.current && activeView.current === mutationView.current && viewVersion.current === mutationVersion.current;
      const skipped = result && 'requested' in result ? result.items.filter((item) => item.status === 'skipped') : [];
      const queuedCount = result && 'needs_detail_count' in result ? result.items.filter((item) => item.status === 'queued').length : 0;
      if (sameView) {
        setSkippedItems(skipped);
        setReanalysisItems(result && 'needs_detail_count' in result ? result.items : []);
        setFeedback(result && 'requested' in result ? { code: 'results.action.applied', args: { count: result.applied } }
          : result && 'needs_detail_count' in result ? queuedCount > 0 ? { code: 'results.action.queued', args: { count: queuedCount } } : null : result);
      }
      if (sameView) setConfirmation(null);
      if (sameView && operation.kind !== 'favorite' && operation.kind !== 'unread' && operation.kind !== 'run' && operation.kind !== 'dismiss-hint') setSelected(skipped.map((item) => item.id));
      await refreshResults();
      if (activeView.current === mutationView.current && (operation.kind === 'delete' || operation.kind === 'delete-all' || operation.kind === 'dismiss' || operation.kind === 'block')) resultsHeading.current?.focus();
    },
    onError: (_error, operation) => {
      if (mounted.current && activeView.current === mutationView.current && viewVersion.current === mutationVersion.current && operation.kind === 'reanalyze' && operation.allFailed) { setConfirmation(null); setSelected([]); }
    },
  });
  function perform(operation: Operation) {
    selectingController.current?.abort();
    setSelecting(false);
    mutationView.current = filtersIdentity;
    mutationVersion.current = viewVersion.current;
    mutation.mutate(operation);
  }
  const reanalysisIDs = confirmation?.kind === 'reanalyze' ? confirmation.ids : [];
  const preview = useQuery({
    queryKey: apiKey('findings', 'reanalysis-preview', reanalysisIDs), enabled: reanalysisIDs.length > 0 && !(confirmation?.kind === 'reanalyze' && confirmation.prepared),
    queryFn: async ({ signal }) => apiData(await api.POST('/api/v1/findings/reanalysis-preview', { body: { ids: reanalysisIDs }, signal })),
    staleTime: 0,
  });

  function changeFilters(patch: Partial<ResultsSearch>) {
    setFeedback(null);
    onSearchChange({ ...search, ...patch, cursor: undefined, finding_id: undefined });
  }
  function ask(next: Confirmation) {
    selectingController.current?.abort();
    setSelecting(false);
    mutation.reset();
    setFeedback(null);
    setSkippedItems([]);
    setReanalysisItems([]);
    setAllProgress(null);
    setConfirmation(next);
  }
  function actOnFinding(finding: Finding, action: FindingAction) {
    if (action === 'favorite' || action === 'restore' || action === 'unread') perform({ kind: action, finding });
    else if (action === 'block') {
      if (finding.listing.seller_id) ask({ kind: 'block', sellerId: finding.listing.seller_id, name: finding.listing.seller_nick ?? finding.listing.seller_id });
    } else if (action === 'reanalyze') ask({ kind: 'reanalyze', ids: [finding.id], single: true });
    else ask({ kind: action, ids: [finding.id] });
  }
  async function selectFiltered(allFailed = false) {
    if (selecting || mutation.isPending) return;
    selectingController.current?.abort();
    const controller = new AbortController();
    const origin = filtersIdentity;
    selectingController.current = controller;
    setSelecting(true);
    setSelectionError(null);
    setFeedback(null);
    try {
      const { cursor: _cursor, ...firstQuery } = findingQuery(search);
      if (allFailed) {
        const ids = new Set<number>();
        const cursors = new Set<string>();
        let cursor: string | undefined;
        do {
          controller.signal.throwIfAborted();
          const response = apiData(await api.GET('/api/v1/findings', {
            params: { query: { ...firstQuery, view: 'all', decision: 'failed', limit: 200, ...(cursor === undefined ? {} : { cursor }) } }, signal: controller.signal,
          }));
          controller.signal.throwIfAborted();
          if (activeView.current !== origin) return;
          for (const item of response.items) ids.add(item.id);
          cursor = response.next_cursor ?? undefined;
          if (cursor !== undefined) {
            if (cursors.has(cursor)) throw new ApiError(0, { code: 'web.invalid_response', args: {} });
            cursors.add(cursor);
          }
        } while (cursor !== undefined);
        const snapshot = [...ids];
        if (snapshot.length === 0) { setFeedback({ code: 'results.reanalyze.no_failed', args: {} }); return; }
        const prepared = await previewReanalysisBatches(api, snapshot, controller.signal);
        if (activeView.current !== origin) return;
        ask({ kind: 'reanalyze', ids: snapshot, single: false, allFailed: true, prepared });
        return;
      }
      const response = apiData(await api.GET('/api/v1/findings', {
        params: { query: { ...firstQuery, limit: 200 } },
        signal: controller.signal,
      }));
      if (controller.signal.aborted) return;
      setSelected(selectableIDs(response.items));
    } catch (error) {
      if (!controller.signal.aborted) setSelectionError(error);
    } finally {
      if (selectingController.current === controller) { setSelecting(false); selectingController.current = null; }
    }
  }

  if (search.finding_id !== undefined) return <FindingDetail findingId={search.finding_id} query={detailQuery(search)} {...(timeZone === undefined ? {} : { timeZone })}
    onClose={() => onSearchChange({ ...search, finding_id: undefined })}
    onNavigate={(findingId, nextTaskId) => onSearchChange({ ...search, finding_id: findingId, task_id: search.task_id ?? nextTaskId })} />;

  const current = search.collection === undefined ? findings : listings;
  const page = current.data;
  const nextCursor = page?.next_cursor;
  const currentCursorIndex = cursorTrail.findIndex((cursor) => cursor === search.cursor);
  const pageNumber = currentCursorIndex >= 0 ? currentCursorIndex + 1 : 1;
  const taskOptions = tasks.data?.pages.flatMap((item) => item.items) ?? [];
  const taskChoices = task.data && !taskOptions.some((option) => option.id === task.data.id) ? [...taskOptions, task.data] : taskOptions;
  const actionError = (mutationView.current === filtersIdentity && viewVersion.current === mutationVersion.current && !isCancellation(mutation.error) ? mutation.error : null) ?? selectionError;
  const nextAllowedAt = actionError instanceof ApiError ? actionError.reason.args.next_allowed_at : null;
  const isReanalysis = confirmation?.kind === 'reanalyze';
  const prepared = confirmation?.kind === 'reanalyze' ? confirmation.prepared : undefined;
  const previewError = isReanalysis && !prepared ? preview.error : null;
  const confirmPending = mutation.isPending || isReanalysis && !prepared && (preview.isPending || preview.isFetching);
  const confirmationTitle = confirmation?.kind === 'block' ? t('results.confirm.block') : confirmation?.kind === 'reanalyze' ? t('results.confirm.reanalyze') : confirmation?.kind === 'dismiss' ? t('results.confirm.dismiss') : t('results.confirm.delete');
  const confirmationDescription = confirmation?.kind === 'delete-all' ? t('results.confirm.delete_all')
    : confirmation?.kind === 'delete' ? t('results.confirm.delete_selected', { count: confirmation.ids.length })
      : confirmation?.kind === 'dismiss' ? t('results.dismiss.global')
        : confirmation?.kind === 'block' ? t('results.block.global', { seller: confirmation.name })
          : prepared && confirmation?.kind === 'reanalyze'
            ? `${mutation.isPending && allProgress ? message(allProgress) : message({ code: 'results.reanalyze.confirm_all', args: { count: confirmation.ids.length, cached: prepared.cached_detail_count, detail: prepared.needs_detail_count } })} ${message({ code: 'results.reanalyze.preview_only', args: {} })} ${t('results.reanalyze.cached')} ${t('results.reanalyze.no_new_notification')}`
            : `${t('results.reanalyze.cached')} ${preview.data ? t('results.reanalyze.quota', { count: preview.data.needs_detail_count }) : ''} ${message({ code: 'results.reanalyze.preview_only', args: {} })} ${t('results.reanalyze.no_new_notification')}`;
  return (
    <div className="min-w-0 space-y-xl">
      <PageHeader title={t('results.title')} description={t('results.description')} actions={<Button busy={current.isFetching} onClick={() => { void current.refetch(); }}>{t('results.refresh')}</Button>} />
      {tasks.error && <InlineMessage tone="danger" action={<Button onClick={() => { void tasks.refetch(); }}>{t('common.retry')}</Button>}>{errorText(tasks.error)}</InlineMessage>}
      {task.error && <InlineMessage tone="danger" action={<Button onClick={() => { void task.refetch(); }}>{t('common.retry')}</Button>}>{errorText(task.error)}</InlineMessage>}
      {settings.error && <InlineMessage tone="warning">{errorText(settings.error)}</InlineMessage>}
      {search.collection === undefined && task.data?.runtime.blocked && <InlineMessage tone="warning">
        <p>{message(task.data.runtime.blocked.reason)}</p>
        {task.data.runtime.blocked.next_allowed_at !== null && <p>{t('results.run.next_at', { time: resultTime(task.data.runtime.blocked.next_allowed_at, locale, timeZone) })}</p>}
        <div className="mt-sm flex flex-wrap gap-md">
          {accountReasons.has(task.data.runtime.blocked.reason.code) && <Link to="/accounts" search={{ account_id: task.data.account_id ?? undefined }} className="inline-flex min-h-control items-center text-accent-text underline underline-offset-4">{t('results.action.accounts')}</Link>}
          {aiReasons.has(task.data.runtime.blocked.reason.code) && <Link to="/settings/ai" className="inline-flex min-h-control items-center text-accent-text underline underline-offset-4">{t('results.action.ai')}</Link>}
          {task.data.runtime.blocked.reason.code === 'run.empty_profile' && <Link to="/tasks" search={{ task_id: task.data.id }} className="inline-flex min-h-control items-center text-accent-text underline underline-offset-4">{t('results.action.edit')}</Link>}
          <Link to="/logs" search={{ task_id: task.data.id }} className="inline-flex min-h-control items-center text-accent-text underline underline-offset-4">{t('results.action.logs')}</Link>
        </div>
      </InlineMessage>}
      {search.collection === undefined && task.data?.no_good_hint && <InlineMessage tone="info" title={t('results.hint.no_good', { count: task.data.no_good_hint.sample_count })}>
        <ul className="space-y-sm">{task.data.no_good_hint.reasons.map((reason, index) => <li key={index}>
          <StoredMessage message={reason.reason} renderText={(text) => t('results.hint.reason', { reason: text, count: reason.count })} />
        </li>)}</ul>
        <div className="mt-md flex flex-wrap gap-sm"><Link to="/tasks" search={{ task_id: task.data.id }} className="inline-flex min-h-control items-center text-accent-text underline underline-offset-4">{t('results.action.edit')}</Link><Button disabled={mutation.isPending} onClick={() => { if (taskId !== undefined) perform({ kind: 'dismiss-hint', taskId }); }}>{t('results.hint.dismiss')}</Button></div>
      </InlineMessage>}
      {search.collection === undefined && taskId !== undefined && <MarketPanel taskId={taskId} />}
      <ResultsFilters search={search} tasks={taskChoices} tasksHaveMore={tasks.hasNextPage} tasksLoading={tasks.isFetchingNextPage}
        onMoreTasks={() => { void tasks.fetchNextPage(); }} onChange={changeFilters} />
      {search.collection === undefined && <p className="sr-only" role="status">{t('results.select.count', { count: selected.length })}</p>}
      <div className="flex flex-wrap items-center justify-between gap-md" ref={resultsHeading} tabIndex={-1}>
        <p role="status" className="text-body tabular-nums text-text-secondary">{page ? t('results.total', { count: page.total }) : t('results.loading')}</p>
        {search.collection === undefined && <div className="flex flex-wrap gap-sm">
          <Button disabled={!page || page.total === 0 || mutation.isPending} busy={selecting} onClick={() => { void selectFiltered(); }}>{t('results.select.filtered')}</Button>
          {search.decision === 'failed' && <Button disabled={taskId === undefined || mutation.isPending} busy={selecting} onClick={() => { void selectFiltered(true); }}>{t('results.reanalyze.all_failed')}</Button>}
          <Button disabled={taskId === undefined || !page || page.total === 0 || mutation.isPending} onClick={() => { if (taskId !== undefined) ask({ kind: 'delete-all', taskId }); }}>{t('results.delete.all_action')}</Button>
          <FindingsCSVExport key={JSON.stringify([taskId, csvQuery])} taskId={taskId} query={csvQuery} />
        </div>}
      </div>
      {selecting && <InlineMessage tone="info" action={<Button onClick={() => { selectingController.current?.abort(); setSelecting(false); }}>{t('common.cancel')}</Button>}>{t('results.loading')}</InlineMessage>}
      {allProgress && <InlineMessage tone={allProgress.code === 'results.reanalyze.stopped' ? 'warning' : 'info'}>{message(allProgress)}</InlineMessage>}
      {feedback && <InlineMessage tone="info">{message(feedback)}{reanalysisItems.some((item) => item.status === 'queued') && taskId !== undefined && <Button disabled={mutation.isPending} onClick={() => perform({ kind: 'run', taskId })}>{t('results.action.run')}</Button>}
        {taskId !== undefined && (reanalysisItems.length > 0 || feedback.code === 'results.run.started' || feedback.code === 'results.run.queued') && <Link to="/logs" search={{ task_id: taskId }} className="inline-flex min-h-control items-center text-accent-text underline underline-offset-4">{t('results.action.logs')}</Link>}
      </InlineMessage>}
      {skippedItems.length > 0 && <InlineMessage tone="warning" title={t('results.action.skipped', { count: skippedItems.length })}>
        <ul className="space-y-sm">{skippedItems.map((item) => <li key={item.id}>#{item.id} · {item.reason ? message(item.reason) : t('web.request_failed')}</li>)}</ul>
      </InlineMessage>}
      {reanalysisItems.length > 0 && <ul className="space-y-sm text-body text-text-secondary">{reanalysisItems.map((item) => <li key={item.finding_id}>
        #{item.finding_id} · {t(item.status === 'queued' ? 'results.reanalysis.queued' : item.status === 'cached' ? 'results.reanalysis.cached' : 'results.reanalysis.rejected')}
        {item.reason && <> · {message(item.reason)}</>}
      </li>)}</ul>}
      {actionError !== null && actionError !== undefined && <InlineMessage tone="danger">
        <p>{errorText(actionError)}</p>
        {typeof nextAllowedAt === 'number' && <p>{t('results.run.next_at', { time: resultTime(nextAllowedAt, locale, timeZone) })}</p>}
        {actionError instanceof ApiError && accountReasons.has(actionError.reason.code) && <Link to="/accounts" search={{ account_id: task.data?.account_id ?? undefined }} className="inline-flex min-h-control items-center text-accent-text underline underline-offset-4">{t('results.action.accounts')}</Link>}
        {actionError instanceof ApiError && aiReasons.has(actionError.reason.code) && <Link to="/settings/ai" className="inline-flex min-h-control items-center text-accent-text underline underline-offset-4">{t('results.action.ai')}</Link>}
        {actionError instanceof ApiError && actionError.reason.code === 'run.empty_profile' && taskId !== undefined && <Link to="/tasks" search={{ task_id: taskId }} className="inline-flex min-h-control items-center text-accent-text underline underline-offset-4">{t('results.action.edit')}</Link>}
        {actionError instanceof ApiError && actionError.fields.map((field) => <p key={field.key}>{message(field.reason)}</p>)}
        {actionError instanceof ApiError && actionError.requestID && <p className="mt-sm break-all text-caption">{t('web.request_id', { request_id: actionError.requestID })}</p>}
      </InlineMessage>}
      {current.isPending ? <StatePanel kind="pending" label={t('results.loading')}><Skeleton className="h-[9rem]" /><Skeleton className="h-[9rem]" /></StatePanel>
        : current.error && !page ? <StatePanel kind="error" title={t('web.request_failed')} description={errorText(current.error)} retry={{ label: t('common.retry'), run: () => { void current.refetch(); } }} />
          : <>
            {current.error && <InlineMessage tone="danger" action={<Button onClick={() => { void current.refetch(); }}>{t('common.retry')}</Button>}>{t('results.refresh_failed')} {errorText(current.error)}</InlineMessage>}
            {page?.total === 0 && <StatePanel kind="empty" title={search.q ? t('results.empty.filtered') : search.collection === 'favorites' ? t('results.empty.favorites') : t('results.empty.title')} description={t('results.empty.description')}
              action={<div className="flex flex-wrap gap-sm"><Button onClick={() => changeFilters({ view: 'all', decision: undefined, q: undefined, price_drop: undefined, over_budget: undefined })}>{t('results.empty.clear')}</Button>
                <Link to="/tasks" search={search.q ? { create_keyword: search.q } : {}} className="inline-flex min-h-control items-center text-accent-text underline underline-offset-4">{search.q ? t('results.empty.create_task', { keyword: search.q }) : t('results.create_task')}</Link></div>} />}
            {search.collection === undefined ? findings.data?.items.map((finding) => <FindingCard key={finding.id} finding={finding} selected={selected.includes(finding.id)} selectionFull={selected.length >= 200} busy={mutation.isPending || selecting} timeZone={timeZone}
              onSelect={(checked) => setSelected((ids) => checked ? ids.length >= 200 || ids.includes(finding.id) ? ids : [...ids, finding.id] : ids.filter((id) => id !== finding.id))}
              onOpen={() => onSearchChange({ ...search, finding_id: finding.id })} onAction={(action) => actOnFinding(finding, action)} />)
              : listings.data?.items.map((listing) => <SavedListingCard key={listing.item_id} listing={listing} onOpen={() => setOpenItem(listing.item_id)} onChanged={(changed) => {
                if (activeView.current === filtersIdentity && (search.collection === 'favorites' && !changed.is_favorite || search.collection === 'dismissed' && !changed.is_dismissed)) resultsHeading.current?.focus();
              }} />)}
            {page && <Pagination page={pageNumber} hasNextPage={nextCursor !== null && nextCursor !== undefined} disabled={current.isFetching} previousLabel={t('results.previous')} nextLabel={t('results.next')}
              pageLabel={t('results.page', { shown: page.items.length, total: page.total })} onPageChange={(nextPage) => {
                if (nextPage > pageNumber && nextCursor) { setCursorTrail((trail) => [...trail.slice(0, pageNumber), nextCursor]); onSearchChange({ ...search, cursor: nextCursor }); }
                else if (nextPage > 0 && nextPage < pageNumber) onSearchChange({ ...search, cursor: cursorTrail[nextPage - 1] });
                resultsHeading.current?.focus();
              }} />}
          </>}
      {selected.length > 0 && search.collection === undefined && <section aria-label={t('results.select.count', { count: selected.length })} className="sticky bottom-0 z-[10] space-y-md border-t border-border bg-raised p-lg pb-[max(var(--space-lg),env(safe-area-inset-bottom))]">
        <p className="text-body tabular-nums">{t('results.select.count', { count: selected.length })} · {t('results.select.limit')}</p>
        <div className="flex flex-wrap gap-sm">
          <Button disabled={mutation.isPending} onClick={() => ask({ kind: 'delete', ids: selected })}>{t('results.bulk.delete')}</Button>
          <Button disabled={mutation.isPending} onClick={() => ask({ kind: 'reanalyze', ids: selected, single: false })}>{t('results.bulk.reanalyze')}</Button>
          <Button disabled={mutation.isPending} onClick={() => ask({ kind: 'dismiss', ids: selected })}>{t('results.bulk.dismiss')}</Button>
          {selected.length >= 2 && selected.length <= 4 ? <Link to="/results/compare" search={{ ids: selected.join(',') }} className="inline-flex min-h-control items-center rounded-control px-md text-body text-accent-text underline underline-offset-4">{t('results.bulk.compare')}</Link> : <Button disabled>{t('results.bulk.compare')}</Button>}
          <Button disabled={mutation.isPending} onClick={() => setSelected([])}>{t('results.select.clear')}</Button>
        </div>
      </section>}
      <ConfirmDialog open={confirmation !== null} onOpenChange={(open) => { if (!open && (!mutation.isPending || confirmation?.kind === 'reanalyze' && confirmation.allFailed)) { reanalysisController.current?.abort(); setConfirmation(null); if (!mutation.isPending) mutation.reset(); } }} title={confirmationTitle}
        description={confirmationDescription} confirmLabel={previewError ? t('common.retry') : t('results.confirm.submit')} cancelLabel={t('common.cancel')}
        pending={confirmPending} destructive={confirmation?.kind === 'delete' || confirmation?.kind === 'delete-all'}
        {...(mutation.error ? { error: errorText(mutation.error) } : previewError ? { error: errorText(previewError) } : {})}
        onConfirm={() => { if (previewError) { void preview.refetch(); return; } if (confirmation && (!isReanalysis || prepared || preview.data)) perform(confirmation); }} />
      <Dialog open={openItem !== null} onOpenChange={(open) => { if (!open) setOpenItem(null); }} title={t('results.listing.title')} closeLabel={t('common.close')}>
        {openItem !== null && <ListingPanel itemId={openItem} />}
      </Dialog>
    </div>
  );
}
