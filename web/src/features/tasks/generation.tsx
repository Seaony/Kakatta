import { useEffect, useRef, useState } from 'react';
import { Link } from '@tanstack/react-router';
import { useQuery } from '@tanstack/react-query';
import { apiData, useApi, useApiError, useMessageText } from '../../api/hooks';
import { ApiError } from '../../api/errors';
import { apiKey } from '../../api/keys';
import type { Schema } from '../../api/types';
import { useAuthentication } from '../../app/auth-state';
import { useTranslation } from '../../i18n';
import { formatMoney } from '../../i18n/format';
import { Button, InlineMessage } from '../../components/ui';
import { moneyInput, profileInput, splitWords, type ProfileDraft, type TaskDraft } from './model';

type Props = {
  taskID?: number;
  keyword: string;
  requirement: string;
  profile: ProfileDraft;
  disabled: boolean;
  onBusyChange(busy: boolean): void;
  onGenerated(result: Schema<'GeneratedDraft'>, requirement: string): void;
  onApply(apply: (current: TaskDraft) => TaskDraft): void;
  onFieldIssues?(issues: Schema<'FieldIssue'>[]): void;
};

export function ProfileGeneration({ taskID, keyword, requirement, profile, disabled, onBusyChange, onGenerated, onApply, onFieldIssues }: Props) {
  const api = useApi(), errorText = useApiError(), messageText = useMessageText();
  const authenticated = useAuthentication()?.authenticated !== false;
  const { t, locale } = useTranslation();
  const providers = useQuery({ queryKey: apiKey('ai-providers'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/ai/providers', { signal })) });
  const operation = useRef<AbortController | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [phase, setPhase] = useState<'idle' | 'pending' | 'slow'>('idle');
  const [result, setResult] = useState<Schema<'GeneratedDraft'> | null>(null);
  const [failure, setFailure] = useState<unknown>(null);
  const [cancelled, setCancelled] = useState(false);
  const [missingKeyword, setMissingKeyword] = useState(false);
  useEffect(() => () => { operation.current?.abort(); operation.current = null; if (timer.current !== null) clearTimeout(timer.current); }, []);
  const cancel = () => {
    operation.current?.abort(); operation.current = null;
    if (timer.current !== null) clearTimeout(timer.current);
    timer.current = null; setPhase('idle'); setCancelled(true); onBusyChange(false);
  };
  useEffect(() => {
    if (!authenticated && operation.current) {
      operation.current.abort(); operation.current = null;
      if (timer.current !== null) clearTimeout(timer.current);
      timer.current = null; setPhase('idle'); setCancelled(true); onBusyChange(false);
    }
  }, [authenticated, onBusyChange]);
  const generate = async () => {
    if (operation.current || disabled || !authenticated) return;
    if (!keyword.trim()) { setMissingKeyword(true); return; }
    setMissingKeyword(false); setFailure(null); setCancelled(false); onFieldIssues?.([]);
    const controller = new AbortController(); operation.current = controller;
    setPhase('pending'); onBusyChange(true);
    const request: Schema<'GenerateProfileInput'> = { keyword: keyword.trim(), requirement, current: profileInput(profile), ...(taskID !== undefined ? { task_id: taskID } : {}) };
    timer.current = setTimeout(() => { if (operation.current === controller) setPhase('slow'); }, 15_000);
    try {
      const generated = apiData(await api.POST('/api/v1/ai/profile', { body: request, signal: controller.signal }));
      if (operation.current !== controller || controller.signal.aborted) return;
      setResult(generated); onGenerated(generated, request.requirement);
    } catch (error) {
      if (operation.current === controller && !controller.signal.aborted) { setFailure(error); onFieldIssues?.(error instanceof ApiError ? error.fields : []); }
    } finally {
      if (operation.current === controller) {
        operation.current = null;
        if (timer.current !== null) clearTimeout(timer.current);
        timer.current = null; setPhase('idle'); onBusyChange(false);
      }
    }
  };
  const configured = providers.data?.some((provider) => provider.enabled) ?? false;
  const suggestions = result?.suggestions;
  const hasSuggestions = suggestions && (suggestions.keyword || suggestions.exclude_words.length > 0 || suggestions.budget_min !== null || suggestions.budget_max !== null);
  return <div className="min-w-0 space-y-md">
    <div className="flex flex-wrap items-center gap-md">
      {configured && <Button busy={phase !== 'idle'} busyLabel={t('tasks.profile.generating')} disabled={disabled || !authenticated} onClick={() => { void generate(); }}>{t('tasks.generate')}</Button>}
      {phase !== 'idle' && <Button onClick={cancel}>{t('common.cancel')}</Button>}
      <Link to="/settings/ai" className="inline-flex min-h-control items-center text-body underline underline-offset-4">{t('tasks.generate.config')}</Link>
    </div>
    {providers.isPending && <p role="status" className="text-body text-text-secondary">{t('common.loading')}</p>}
    {providers.error && <InlineMessage tone="warning" action={<Button onClick={() => { void providers.refetch(); }}>{t('common.retry')}</Button>}>{errorText(providers.error)}</InlineMessage>}
    {providers.isSuccess && !configured && <InlineMessage>{t('tasks.profile.manual_without_ai')}</InlineMessage>}
    {phase === 'slow' && <p role="status" className="text-body text-text-secondary">{t('tasks.profile.still_generating')}</p>}
    {cancelled && <p role="status" className="text-body text-text-secondary">{t('tasks.profile.cancelled')}</p>}
    {missingKeyword && <InlineMessage tone="danger">{t('tasks.validation.required')}</InlineMessage>}
    {failure !== null && <InlineMessage tone="danger"><p>{errorText(failure)}</p>{failure instanceof ApiError && failure.requestID && <p>{t('web.request_id', { request_id: failure.requestID })}</p>}{failure instanceof ApiError && failure.fields.map((field) => <p key={field.key}>{messageText(field.reason)}</p>)}</InlineMessage>}
    {result?.warnings.map((warning, index) => <InlineMessage key={index} tone="warning">{messageText(warning)}</InlineMessage>)}
    {hasSuggestions && suggestions && <section className="min-w-0 space-y-md" aria-label={t('tasks.suggestions.title')}>
      <h3 className="text-body font-semibold">{t('tasks.suggestions.title')}</h3>
      {suggestions.keyword && <InlineMessage action={<Button disabled={disabled || phase !== 'idle'} onClick={() => onApply((current) => ({ ...current, keyword: suggestions.keyword ?? current.keyword }))}>{t('tasks.suggestions.apply')}</Button>}>{t('tasks.suggestions.keyword', { keyword: suggestions.keyword })}</InlineMessage>}
      {suggestions.exclude_words.length > 0 && <InlineMessage action={<Button disabled={disabled || phase !== 'idle'} onClick={() => onApply((current) => ({ ...current, exclude_keywords_text: splitWords(`${current.exclude_keywords_text}\n${suggestions.exclude_words.join('\n')}`).join('\n') }))}>{t('tasks.suggestions.apply')}</Button>}>{t('tasks.suggestions.exclusions', { words: suggestions.exclude_words.join('、') })}</InlineMessage>}
      {(suggestions.budget_min !== null || suggestions.budget_max !== null) && <InlineMessage action={<Button disabled={disabled || phase !== 'idle'} onClick={() => onApply((current) => ({ ...current, budget_min: moneyInput(suggestions.budget_min), budget_max: moneyInput(suggestions.budget_max) }))}>{t('tasks.suggestions.apply')}</Button>}>{t('tasks.suggestions.budget', { min: suggestions.budget_min === null ? t('tasks.range.unlimited') : formatMoney(suggestions.budget_min, locale), max: suggestions.budget_max === null ? t('tasks.range.unlimited') : formatMoney(suggestions.budget_max, locale) })}</InlineMessage>}
    </section>}
  </div>;
}
