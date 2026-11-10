import { useEffect, useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { apiData, useApi, useMessageText, useTimeZone } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import type { Schema } from '../../api/types';
import { ApiError } from '../../api/errors';
import { Button, ConfirmDialog, Dialog, FormSection, StatePanel } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { ProviderForm } from './provider-form';
import { ProviderRow } from './provider-row';
import { TrialPanel } from './trial-panel';
import { AIErrorNotice, useAIErrorDescription } from './error-notice';
import type { ProviderDraft } from './draft';
import { useAuthentication } from '../../app/auth-state';

type ActionContext = { signal: AbortSignal; isCurrent(): boolean };

export type ProviderSetupProps = {
  embedded?: boolean;
  providerId?: number;
  onSaved?(providerID: number): void;
  onAvailabilityChange?(hasUsableProvider: boolean): void;
};

function draftOf(provider: Schema<'ProviderView'>): ProviderDraft {
  return { name: provider.name, preset: provider.preset ?? 'custom', baseURL: provider.base_url, visionModel: provider.vision_model, textModel: provider.text_model ?? '', enabled: provider.enabled, keyAction: 'keep', keyValue: '' };
}

export function ProviderSetup({ embedded = false, providerId, onSaved, onAvailabilityChange }: ProviderSetupProps) {
  const api = useApi();
  const authentication = useAuthentication();
  const { t } = useTranslation();
  const messageText = useMessageText();
  const errorText = useAIErrorDescription();
  const timeZone = useTimeZone();
  const queryClient = useQueryClient();
  const providers = useQuery({ queryKey: apiKey('ai-providers'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/ai/providers', { signal })) });
  const [editor, setEditor] = useState<{ provider: Schema<'ProviderView'> | null } | null>(null);
  const [trial, setTrial] = useState<Schema<'ProviderView'> | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Schema<'ProviderView'> | null>(null);
  const [discard, setDiscard] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [warnings, setWarnings] = useState<Schema<'LocalizedMessage'>[]>([]);
  const [stopped, setStopped] = useState(false);
  const lock = useRef(false);
  const active = useRef<AbortController | null>(null);
  const generation = useRef(0);
  const mounted = useRef(true);
  const authenticated = useRef(authentication?.authenticated);
  authenticated.current = authentication?.authenticated;
  const focused = useRef<number | undefined>(undefined);
  const initialized = useRef(false);
  const hasUsableProvider = providers.data?.some((provider) => provider.enabled && provider.error_state === null) ?? false;
  function stopWaiting() {
    const hadRequest = active.current !== null;
    generation.current++; active.current?.abort(); active.current = null; lock.current = false;
    if (mounted.current) { setBusy(false); if (hadRequest) { setStopped(true); setError(null); } }
  }
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; generation.current++; active.current?.abort(); active.current = null; lock.current = false; };
  }, []);
  useEffect(() => api.subscribeAuthenticationLost(stopWaiting), [api]);
  useEffect(() => { if (authentication?.authenticated === false) stopWaiting(); }, [authentication?.authenticated]);
  useEffect(() => { onAvailabilityChange?.(hasUsableProvider); }, [hasUsableProvider, onAvailabilityChange]);
  useEffect(() => {
    if (!embedded || !providers.data || initialized.current) return;
    initialized.current = true;
    if (providers.data.length === 0) setEditor({ provider: null });
  }, [embedded, providers.data]);
  useEffect(() => {
    if (providerId === undefined || focused.current === providerId || !providers.data || !providers.isSuccess || providers.isFetching) return;
    focused.current = providerId;
    const provider = providers.data.find((item) => item.id === providerId);
    if (provider) { setEditor({ provider }); setDirty(false); setError(null); }
  }, [providerId, providers.data, providers.isSuccess, providers.isFetching]);
  const ordered = [...(providers.data ?? [])].sort((left, right) => left.sort_index - right.sort_index || left.id - right.id);
  async function refreshFacts() {
    await Promise.all(['ai-providers', 'ai-usage', 'system-events', 'overview', 'tasks'].map((resource) => queryClient.invalidateQueries({ queryKey: apiKey(resource) })));
  }
  async function perform(action: (context: ActionContext) => Promise<void>) {
    if (lock.current || authenticated.current === false) return;
    const controller = new AbortController(), ticket = ++generation.current;
    const isCurrent = () => mounted.current && generation.current === ticket && !controller.signal.aborted && authenticated.current !== false;
    active.current = controller; lock.current = true; setBusy(true); setError(null); setWarnings([]); setStopped(false);
    try { await action({ signal: controller.signal, isCurrent }); }
    catch (failure) { if (isCurrent()) setError(failure); }
    finally { if (generation.current === ticket && mounted.current) { active.current = null; lock.current = false; setBusy(false); } }
  }
  async function save(draft: ProviderDraft) {
    const currentEditor = editor;
    if (!currentEditor) return;
    await perform(async ({ signal, isCurrent }) => {
      const body: Schema<'ProviderInput'> = {
        name: draft.name, preset: draft.preset || null, base_url: draft.baseURL,
        vision_model: draft.visionModel, text_model: draft.textModel || null, enabled: draft.enabled,
        ...(draft.keyAction === 'remove' ? { api_key: '' } : draft.keyAction === 'replace' ? { api_key: draft.keyValue } : {}),
      };
      const result = currentEditor.provider
        ? apiData(await api.PUT('/api/v1/ai/providers/{provider_id}', { params: { path: { provider_id: currentEditor.provider.id } }, body, signal }))
        : apiData(await api.POST('/api/v1/ai/providers', { body, signal }));
      if (!isCurrent()) return;
      active.current = null;
      queryClient.setQueryData<Schema<'ProviderView'>[]>(apiKey('ai-providers'), (old) => [...(old ?? []).filter((item) => item.id !== result.provider.id), result.provider]);
      setWarnings(result.warnings); setDirty(false); setEditor(null);
      onSaved?.(result.provider.id);
      await refreshFacts();
    });
  }
  function closeEditor() {
    if (busy) { stopWaiting(); return; }
    if (dirty) setDiscard(true); else { setEditor(null); setError(null); }
  }
  function move(index: number, direction: -1 | 1) {
    const ids = ordered.map((provider) => provider.id);
    const moving = ids[index], other = ids[index + direction];
    if (moving === undefined || other === undefined) return;
    ids[index] = other; ids[index + direction] = moving;
    void perform(async ({ signal, isCurrent }) => {
      const next = apiData(await api.PUT('/api/v1/ai/providers/order', { body: { ids }, signal }));
      if (!isCurrent()) return;
      active.current = null;
      queryClient.setQueryData(apiKey('ai-providers'), next);
    });
  }
  const fields = error instanceof ApiError ? Object.fromEntries(error.fields.map((field) => [field.key, messageText(field.reason)])) : {};
  const editorForm = editor && <ProviderForm key={editor.provider?.id ?? 'new'} {...(editor.provider ? { initial: draftOf(editor.provider) } : {})} apiKeySet={editor.provider?.api_key_set ?? false} pending={busy} {...(error ? { error: errorText(error) } : {})} fieldErrors={fields} onDirtyChange={setDirty} onSave={(draft) => { void save(draft); }} onCancel={closeEditor} />;
  const stoppedNotice = stopped && <div className="space-y-sm"><p role="status" className="text-body text-warning">{t('web.ai.save_wait_stopped')}</p><Button onClick={() => { void providers.refetch(); }}>{t('web.ai.reload_providers')}</Button></div>;
  return <FormSection title={t('web.ai.providers')} description={t('web.ai.failover_hint')} actions={<Button variant="primary" disabled={busy} onClick={() => { focused.current = providerId; setEditor({ provider: null }); setDirty(false); setError(null); }}>{t('web.ai.add_provider')}</Button>}>
    {providers.isPending && !providers.data && <StatePanel kind="pending" label={t('common.loading')} />}
    {providers.isError && !providers.data && <StatePanel kind="error" title={t('web.error.title')} description={errorText(providers.error)} retry={{ label: t('common.retry'), run: () => { void providers.refetch(); } }} />}
    {providers.data && ordered.length === 0 && <StatePanel kind="empty" title={t('web.ai.no_providers')} />}
    {providers.data && ordered.length > 0 && !hasUsableProvider && <p role="status" className="text-body text-warning">{t('web.ai.no_available')}</p>}
    {providerId !== undefined && providers.data && providers.isSuccess && !providers.isFetching && !providers.data.some((provider) => provider.id === providerId) && <p role="status" className="text-body text-warning">{t('web.ai.provider_missing')}</p>}
    {ordered.map((provider, index) => <ProviderRow key={provider.id} name={provider.name} baseURL={provider.base_url} visionModel={provider.vision_model} textModel={provider.text_model} enabled={provider.enabled} reason={provider.error_state} errorAt={provider.error_at} {...(timeZone ? { timeZone } : {})} pending={busy} first={index === 0} last={index === ordered.length - 1}
      onEdit={() => { focused.current = providerId; setEditor({ provider }); setDirty(false); setError(null); }} onTest={() => { setTrial(provider); setError(null); }} onMove={(direction) => move(index, direction)} onDelete={() => { setDeleteTarget(provider); setError(null); }}
      onEnabled={(enabled) => { void perform(async ({ signal, isCurrent }) => {
        const next = apiData(await api.PUT('/api/v1/ai/providers/{provider_id}/enabled', { params: { path: { provider_id: provider.id } }, body: { enabled }, signal }));
        if (!isCurrent()) return;
        active.current = null;
        queryClient.setQueryData<Schema<'ProviderView'>[]>(apiKey('ai-providers'), (old) => old?.map((item) => item.id === next.id ? next : item));
        await refreshFacts();
      }); }} />)}
    {providers.data && providers.isError && <div className="space-y-sm"><AIErrorNotice error={providers.error} /><Button onClick={() => { void providers.refetch(); }}>{t('common.retry')}</Button></div>}
    {error !== null && !editor && !deleteTarget && <div className="space-y-sm"><AIErrorNotice error={error} /><Button busy={providers.isFetching} onClick={() => { void providers.refetch().then((result) => { if (!result.isError) setError(null); }); }}>{t('web.ai.reload_providers')}</Button></div>}
    {warnings.map((warning, index) => <p key={index} role="status" className="text-body text-warning">{messageText(warning)}</p>)}
    {!editor && stoppedNotice}
    {embedded && editorForm}
    {embedded && editor && stoppedNotice}
    <Dialog open={editor !== null && !embedded} onOpenChange={(open) => { if (!open) closeEditor(); }} title={t(editor?.provider ? 'web.ai.edit_provider' : 'web.ai.add_provider')} closeLabel={t('common.close')}>
      {!embedded && editorForm}
      {!embedded && stoppedNotice}
    </Dialog>
    <ConfirmDialog open={discard} onOpenChange={setDiscard} title={t('web.ai.discard_title')} description={t('web.ai.discard_description')} confirmLabel={t('web.ai.discard_confirm')} cancelLabel={t('common.cancel')} onConfirm={() => { setEditor(null); setDirty(false); setDiscard(false); setError(null); }} />
    <ConfirmDialog open={deleteTarget !== null} onOpenChange={(open) => { if (!open && !busy) setDeleteTarget(null); }} title={t('web.ai.delete_provider_title')} description={`${deleteTarget?.name ?? ''}\n${t('web.ai.delete_provider_description')}`} confirmLabel={t('web.ai.delete')} cancelLabel={t('common.cancel')} pending={busy} destructive {...(error ? { error: errorText(error) } : {})} onConfirm={() => {
      const target = deleteTarget;
      if (!target) return;
      void perform(async ({ signal, isCurrent }) => {
        await api.DELETE('/api/v1/ai/providers/{provider_id}', { params: { path: { provider_id: target.id } }, signal });
        if (!isCurrent()) return;
        active.current = null;
        queryClient.setQueryData<Schema<'ProviderView'>[]>(apiKey('ai-providers'), (old) => old?.filter((item) => item.id !== target.id));
        setDeleteTarget(null); await refreshFacts();
      });
    }} />
    <Dialog open={trial !== null} onOpenChange={(open) => { if (!open) setTrial(null); }} title={t('web.ai.test')} {...(trial ? { description: trial.name } : {})} closeLabel={t('common.close')}>
      {trial && <TrialPanel providerId={trial.id} onFinished={() => { void refreshFacts(); }} />}
    </Dialog>
  </FormSection>;
}
