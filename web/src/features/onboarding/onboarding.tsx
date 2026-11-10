import { useEffect, useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { ApiError } from '../../api/errors';
import { apiData, useApi } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import { Button, InlineMessage, PageHeader, Skeleton, StatePanel, StatusBadge } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { AccountConnect } from '../accounts';
import { ProviderSetup } from '../ai';
import { ChannelSetup } from '../notifications';
import { TaskEditor } from '../tasks';
import { AuthError } from './auth-error';
import { useCredentialAction } from './auth-state';

const stages = ['account', 'ai', 'notifications', 'task', 'complete'] as const;
type Stage = typeof stages[number];
const titles = {
  account: 'web.onboarding.account_title',
  ai: 'web.onboarding.ai_title',
  notifications: 'web.onboarding.notify_title',
  task: 'web.onboarding.task_title',
  complete: 'web.onboarding.complete_title',
} as const;

export function OnboardingPage({ onCompleted }: { onCompleted?: () => void }) {
  const { t } = useTranslation();
  const api = useApi();
  const queryClient = useQueryClient();
  const finish = useCredentialAction();
  const settings = useQuery({ queryKey: apiKey('settings'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings', { signal })) });
  const done = settings.data?.values['general.onboarding_done'];
  const tasks = useQuery({
    queryKey: apiKey('tasks', 'onboarding'), enabled: done === false,
    queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/tasks', { signal, params: { query: { limit: 1 } } })),
  });
  const [selectedStage, setSelectedStage] = useState<Stage | null>(null);
  const [visited, setVisited] = useState<ReadonlySet<Stage>>(new Set());
  const [skipped, setSkipped] = useState<ReadonlySet<Stage>>(new Set());
  const [furthest, setFurthest] = useState(0);
  const [accountReady, setAccountReady] = useState(false);
  const [providerReady, setProviderReady] = useState(false);
  const [channelReady, setChannelReady] = useState(false);
  const [createdTask, setCreatedTask] = useState(false);
  const [draftStarted, setDraftStarted] = useState(false);
  const hasTask = createdTask || (tasks.data?.total ?? 0) > 0;
  const current: Stage = selectedStage ?? (hasTask ? 'complete' : 'account');
  const currentIndex = stages.indexOf(current);
  const availableThrough = Math.max(furthest, hasTask ? stages.length - 1 : 0);
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => { heading.current?.focus(); }, [current, settings.isPending, tasks.isPending]);
  useEffect(() => {
    if (done === false && tasks.data && selectedStage === null) setSelectedStage(tasks.data.total > 0 ? 'complete' : 'account');
  }, [done, tasks.data, selectedStage]);

  function goTo(next: Stage) {
    if (next === 'task' && !hasTask) setDraftStarted(true);
    setVisited((previous) => new Set([...previous, current, next]));
    setFurthest((previous) => Math.max(previous, stages.indexOf(next)));
    setSelectedStage(next);
    finish.clearError();
  }

  function skip(stage: 'ai' | 'notifications') {
    setSkipped((previous) => new Set([...previous, stage]));
    goTo(stage === 'ai' ? 'notifications' : 'task');
  }

  async function complete() {
    if (!hasTask) return;
    const snapshot = await finish.run(async (signal) => {
      const result = apiData(await api.PATCH('/api/v1/settings', { signal, body: { values: { 'general.onboarding_done': true } } }));
      if (result.values['general.onboarding_done'] !== true) throw new ApiError(0, { code: 'web.invalid_response', args: {} });
      return result;
    });
    if (!snapshot) return;
    queryClient.setQueryData(apiKey('settings'), snapshot);
    onCompleted?.();
  }

  async function checkCompletion() {
    const snapshot = await finish.run(async (signal) => apiData(await api.GET('/api/v1/settings', { signal })));
    if (!snapshot) return;
    queryClient.setQueryData(apiKey('settings'), snapshot);
    if (snapshot.values['general.onboarding_done'] === true) onCompleted?.();
  }

  if (settings.isPending || (done === false && tasks.isPending)) {
    return <StatePanel kind="pending" label={t('common.loading')}><Skeleton /><Skeleton /></StatePanel>;
  }
  if (!settings.data || (done === false && !tasks.data)) {
    return <div className="space-y-lg"><AuthError error={settings.error ?? tasks.error} /><Button onClick={() => { void settings.refetch(); if (done === false) void tasks.refetch(); }}>{t('common.retry')}</Button></div>;
  }
  if (typeof done !== 'boolean') return <div className="space-y-lg"><AuthError error={new ApiError(0, { code: 'web.invalid_response', args: {} })} /><Button onClick={() => { void settings.refetch(); }}>{t('common.retry')}</Button></div>;
  if (done) {
    return <div className="space-y-xl"><PageHeader title={t('web.onboarding.complete_title')} /><InlineMessage tone="positive">{t('web.onboarding.already_complete')}</InlineMessage><div className="flex flex-wrap gap-lg"><Link to="/overview">{t('web.onboarding.open_overview')}</Link><Link to="/tasks">{t('web.onboarding.open_tasks')}</Link></div></div>;
  }

  return (
    <div className="space-y-xl">
      <PageHeader title={t('web.onboarding.title')} description={t('web.onboarding.step', { current: Math.min(currentIndex + 2, 5), total: 5 })} />
      <div className="grid min-w-0 gap-xxl lg:grid-cols-[minmax(0,14rem)_minmax(0,1fr)]">
        <nav aria-label={t('web.onboarding.steps_label')}>
          <ol className="flex min-w-0 flex-wrap gap-sm lg:flex-col">
            <li className="hidden items-center justify-between gap-sm text-body text-text-secondary lg:flex">
              <span>{t('web.onboarding.admin_title')}</span><StatusBadge tone="positive">{t('web.onboarding.step_done')}</StatusBadge>
            </li>
            {stages.filter((stage) => stage !== 'complete').map((stage, index) => {
              const finished = stage === 'account' ? accountReady : stage === 'ai' ? providerReady : stage === 'notifications' ? channelReady : hasTask;
              return <li key={stage} className="flex min-w-0 items-center gap-sm">
                <Button variant={current === stage ? 'secondary' : 'plain'} className="justify-start text-left"
                  aria-current={current === stage ? 'step' : undefined} disabled={index > availableThrough || finish.pending}
                  onClick={() => goTo(stage)}>{t(titles[stage])}</Button>
                {finished ? <StatusBadge tone="positive">{t('web.onboarding.step_done')}</StatusBadge>
                  : skipped.has(stage) ? <StatusBadge>{t('web.onboarding.step_skipped')}</StatusBadge> : null}
              </li>;
            })}
          </ol>
        </nav>
        <section className="min-w-0 space-y-xl" aria-label={t(titles[current])}>
          <h2 ref={heading} className="text-section font-semibold text-text-primary" tabIndex={-1}>{t(titles[current])}</h2>
          {(current === 'account' || visited.has('account')) && <div hidden={current !== 'account'} className="space-y-lg">
            <AccountConnect onConnected={() => setAccountReady(true)} />
            <Button variant="primary" disabled={!accountReady} onClick={() => goTo('ai')}>{t('web.onboarding.continue')}</Button>
          </div>}
          {(current === 'ai' || visited.has('ai')) && <div hidden={current !== 'ai'} className="space-y-lg">
            <p className="text-body text-text-secondary">{t('web.onboarding.ai_later')}</p>
            <ProviderSetup embedded onAvailabilityChange={setProviderReady} />
            <div className="flex flex-wrap gap-sm"><Button variant="primary" disabled={!providerReady} onClick={() => goTo('notifications')}>{t('web.onboarding.continue')}</Button><Button onClick={() => skip('ai')}>{t('web.onboarding.skip_ai')}</Button></div>
          </div>}
          {(current === 'notifications' || visited.has('notifications')) && <div hidden={current !== 'notifications'} className="space-y-lg">
            <p className="text-body text-text-secondary">{t('web.onboarding.notify_later')}</p>
            <ChannelSetup onSaved={() => setChannelReady(true)} onAvailabilityChange={(available) => setChannelReady(available === true)} />
            <div className="flex flex-wrap gap-sm"><Button variant="primary" disabled={!channelReady} onClick={() => goTo('task')}>{t('web.onboarding.continue')}</Button><Button onClick={() => skip('notifications')}>{t('web.onboarding.skip_notify')}</Button></div>
          </div>}
          {(current === 'task' || visited.has('task')) && <div hidden={current !== 'task'} className="space-y-lg">
            {hasTask && <InlineMessage tone="info" action={<Button onClick={() => goTo('complete')}>{t('web.onboarding.continue')}</Button>}>{t('web.onboarding.task_exists')}</InlineMessage>}
            {!createdTask && (draftStarted || !hasTask) && <TaskEditor embedded onSaved={() => { setCreatedTask(true); void queryClient.invalidateQueries({ queryKey: apiKey('tasks') }); goTo('complete'); }} onCancel={() => goTo('notifications')} />}
          </div>}
          {current === 'complete' && <div className="space-y-lg">
            <InlineMessage tone="positive">{t(createdTask ? 'web.onboarding.first_task_saved' : 'web.onboarding.task_exists')}</InlineMessage>
            <p className="text-body text-text-secondary">{t('web.onboarding.finish_help')}</p>
            <AuthError error={finish.error} />
            {finish.uncertain && <InlineMessage tone="warning" action={<Button onClick={() => { void checkCompletion(); }} disabled={finish.pending}>{t('web.auth.check_status')}</Button>}>{t('web.request_uncertain')}</InlineMessage>}
            <div className="flex flex-wrap gap-sm"><Button variant="primary" busy={finish.pending} busyLabel={t('web.onboarding.finishing')} onClick={() => { void complete(); }}>{t('web.onboarding.finish')}</Button>{finish.pending && <Button onClick={finish.cancel}>{t('common.cancel')}</Button>}</div>
          </div>}
          {currentIndex > 0 && current !== 'complete' && <Button variant="plain" onClick={() => { const previous = stages[currentIndex - 1]; if (previous) goTo(previous); }}>{t('web.onboarding.back')}</Button>}
          {(settings.error || tasks.error) && <AuthError error={settings.error ?? tasks.error} />}
        </section>
      </div>
    </div>
  );
}
