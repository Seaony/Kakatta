import { useEffect, useRef, useState } from 'react';
import { Link } from '@tanstack/react-router';
import { useQueryClient } from '@tanstack/react-query';
import { useApi, useApiError, useMessageText } from '../../api/hooks';
import { ApiError } from '../../api/errors';
import { apiKey } from '../../api/keys';
import type { Schema } from '../../api/types';
import { useAuthentication } from '../../app/auth-state';
import { useTranslation } from '../../i18n';
import { Button, ConfirmDialog, InlineMessage } from '../../components/ui';
import { previewReanalysisBatches, queueReanalysisBatches, type ReanalysisProgress } from '../results/reanalysis-batches';

export function TaskReanalysis({ taskID, effects }: { taskID: number; effects: Schema<'TaskSaveEffects'> }) {
  const { t } = useTranslation();
  const [active, setActive] = useState<'needs' | 'title' | null>(null);
  return <div className="space-y-lg">
    {effects.needs_reanalysis_ids.length > 0 && <ReanalysisSet taskID={taskID} ids={effects.needs_reanalysis_ids} description={t('tasks.profile.reanalyze_needed', { count: effects.needs_reanalysis_count })} disabled={active === 'title'} onBusyChange={(busy) => setActive(busy ? 'needs' : null)} />}
    {effects.title_rejected_ids.length > 0 && <ReanalysisSet taskID={taskID} ids={effects.title_rejected_ids} description={t('tasks.profile.spec_reanalysis', { count: effects.title_rejected_count })} disabled={active === 'needs'} onBusyChange={(busy) => setActive(busy ? 'title' : null)} />}
  </div>;
}

function ReanalysisSet({ taskID, ids, description, disabled, onBusyChange }: { taskID: number; ids: readonly number[]; description: string; disabled: boolean; onBusyChange(busy: boolean): void }) {
  const api = useApi(), errorText = useApiError(), messageText = useMessageText(), queryClient = useQueryClient();
  const authenticated = useAuthentication()?.authenticated !== false;
  const { t } = useTranslation();
  const operation = useRef<AbortController | null>(null);
  const progressRef = useRef<ReanalysisProgress | null>(null);
  const [phase, setPhase] = useState<'idle' | 'previewing' | 'ready' | 'queuing'>('idle');
  const [preview, setPreview] = useState<Schema<'ReanalysisPreview'> | null>(null);
  const [progress, setProgress] = useState<ReanalysisProgress | null>(null);
  const [failure, setFailure] = useState<unknown>(null);
  const [open, setOpen] = useState(false);
  const stop = useRef<(authenticationLost?: boolean) => void>(() => {});
  stop.current = (authenticationLost = false) => {
    if (!operation.current && !open) return;
    operation.current?.abort(); operation.current = null;
    const stopped: ReanalysisProgress = { ...(progressRef.current ?? { items: [], total: ids.length, uncertain: 0, remaining: ids.length }), stopped: true };
    progressRef.current = stopped; setProgress(stopped); setOpen(false); setPhase('idle'); onBusyChange(false);
    if (authenticationLost) setFailure(new ApiError(401, { code: 'auth.unauthenticated', args: {} }));
  };
  useEffect(() => {
    const unsubscribe = api.subscribeAuthenticationLost(() => stop.current(true));
    return () => { const controller = operation.current; operation.current = null; controller?.abort(); unsubscribe(); };
  }, [api]);
  useEffect(() => { if (!authenticated) stop.current(true); }, [authenticated]);

  const inspect = async () => {
    if (operation.current || phase !== 'idle' || disabled || !authenticated) return;
    const controller = new AbortController(); operation.current = controller;
    setFailure(null); setPreview(null); setProgress(null); progressRef.current = null; setPhase('previewing'); onBusyChange(true);
    try {
      const prepared = await previewReanalysisBatches(api, ids, controller.signal);
      if (operation.current !== controller || controller.signal.aborted) return;
      setPreview(prepared); setOpen(true); setPhase('ready');
    } catch (error) {
      if (operation.current === controller && !controller.signal.aborted) { setFailure(error); setPhase('idle'); onBusyChange(false); }
    } finally { if (operation.current === controller) operation.current = null; }
  };

  const enqueue = async () => {
    if (!preview || phase !== 'ready' || operation.current || !authenticated) return;
    const controller = new AbortController(); operation.current = controller;
    setFailure(null); setPhase('queuing');
    let confirmed = false;
    try {
      await queueReanalysisBatches(api, ids, controller.signal, (next) => {
        if (operation.current !== controller) return;
        progressRef.current = next; setProgress(next); confirmed = next.items.length > 0;
      });
      if (operation.current !== controller || controller.signal.aborted) return;
      setOpen(false); setPhase('idle'); onBusyChange(false);
    } catch (error) {
      if (operation.current === controller && !controller.signal.aborted) { setFailure(error); setOpen(false); setPhase('idle'); onBusyChange(false); }
    } finally {
      if (operation.current === controller) operation.current = null;
      if (confirmed) for (const resource of ['findings', 'tasks', 'overview']) void queryClient.invalidateQueries({ queryKey: apiKey(resource) });
    }
  };
  const queued = progress?.items.filter((item) => item.status === 'queued').length ?? 0;
  const progressText = progress ? progress.stopped
    ? t('results.reanalyze.stopped', { confirmed: progress.items.length, queued, uncertain: progress.uncertain, remaining: progress.remaining })
    : t('results.reanalyze.progress', { confirmed: progress.items.length, total: progress.total, queued }) : '';
  const confirmation = preview ? `${description} ${t('tasks.effects.reanalysis_confirm', { count: preview.requested_count, cached: preview.cached_detail_count, detail: preview.needs_detail_count })} ${t('results.reanalyze.preview_only')} ${t('web.detail.reanalysis_cost')} ${t('results.reanalyze.no_new_notification')}` : '';
  return <section className="min-w-0 space-y-md" aria-label={description}>
    <InlineMessage tone="info" action={<div className="flex flex-wrap gap-sm"><Button disabled={disabled || !authenticated || phase !== 'idle'} busy={phase === 'previewing'} busyLabel={t('common.loading')} onClick={() => { void inspect(); }}>{t('results.action.reanalyze')}</Button>{phase === 'previewing' && <Button onClick={() => stop.current()}>{t('common.cancel')}</Button>}</div>}>{description}</InlineMessage>
    {failure !== null && <InlineMessage tone="danger"><p>{errorText(failure)}</p>{failure instanceof ApiError && failure.requestID && <p>{t('web.request_id', { request_id: failure.requestID })}</p>}</InlineMessage>}
    {progress && <InlineMessage tone={progress.stopped ? 'warning' : 'info'}>{progressText}{queued > 0 && <p>{t('results.action.queued', { count: queued })}</p>}</InlineMessage>}
    {progress && progress.items.length > 0 && <ul className="space-y-sm text-body text-text-secondary">{progress.items.map((item) => <li key={item.finding_id}><Link to="/results" search={{ task_id: taskID, finding_id: item.finding_id }} className="underline underline-offset-4">#{item.finding_id}</Link> · {t(item.status === 'queued' ? 'results.reanalysis.queued' : item.status === 'cached' ? 'results.reanalysis.cached' : 'results.reanalysis.rejected')}{item.reason && <> · {messageText(item.reason)}</>}</li>)}</ul>}
    <Link to="/results" search={{ task_id: taskID, view: 'all' }} className="inline-flex min-h-control items-center text-body underline underline-offset-4">{t('tasks.results')}</Link>
    <ConfirmDialog open={open} onOpenChange={(next) => { if (!next) stop.current(); }} title={t('results.confirm.reanalyze')} description={phase === 'queuing' && progressText ? progressText : confirmation} confirmLabel={t('results.confirm.submit')} cancelLabel={t('common.cancel')} pending={phase === 'queuing'} onConfirm={() => { void enqueue(); }} />
  </section>;
}
