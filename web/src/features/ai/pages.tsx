import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiData, useApi, useMessageText, useTimeZone } from '../../api/hooks';
import { ApiError } from '../../api/errors';
import { apiKey } from '../../api/keys';
import type { Schema } from '../../api/types';
import { ConfirmDialog, PageHeader, StatePanel, Tabs } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { formatDateTime } from '../../i18n/format';
import { AISettingsPanel } from './settings-panel';
import { ProviderSetup } from './provider-setup';
import { PromptEditor } from './prompt-editor';
import { promptKinds, type PromptKind } from './draft';
import { AIUsagePanel } from './usage-panel';
import { AIBudgetPanel } from './budget-panel';
import { AIErrorNotice, useAIErrorDescription } from './error-notice';

const promptLabels = {
  analysis: 'web.ai.prompt_analysis', title_screen: 'web.ai.prompt_title_screen', profile: 'web.ai.prompt_profile', market: 'web.ai.prompt_market',
} as const;

export function AIPage({ providerId }: { providerId?: number }) {
  const { t } = useTranslation();
  return <div className="min-w-0 space-y-xxl">
    <PageHeader title={t('web.ai.title')} description={t('web.ai.protocol_hint')} />
    <AIBudgetPanel />
    <ProviderSetup {...(providerId !== undefined ? { providerId } : {})} />
    <AISettingsPanel />
    <AIUsagePanel />
  </div>;
}

export function PromptsPage() {
  const api = useApi();
  const { t, locale } = useTranslation();
  const errorText = useAIErrorDescription();
  const messageText = useMessageText();
  const timeZone = useTimeZone();
  const queryClient = useQueryClient();
  const [kind, setKind] = useState<PromptKind>('analysis');
  const [dirty, setDirty] = useState(false);
  const [pendingKind, setPendingKind] = useState<PromptKind | null>(null);
  const prompts = useQuery({ queryKey: apiKey('prompts'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/prompts', { signal })) });
  function store(next: Schema<'PromptView'>) {
    queryClient.setQueryData<Schema<'PromptView'>[]>(apiKey('prompts'), (old) => [...(old ?? []).filter((item) => item.kind !== next.kind), next]);
  }
  const save = useMutation({ mutationFn: async ({ kind: target, ...body }: Schema<'PromptSaveRequest'> & { kind: PromptKind }) => apiData(await api.PUT('/api/v1/prompts/{kind}', { params: { path: { kind: target } }, body })), onSuccess: store });
  const restore = useMutation({ mutationFn: async (target: PromptKind) => apiData(await api.DELETE('/api/v1/prompts/{kind}', { params: { path: { kind: target } } })), onSuccess: store });
  const document = prompts.data?.find((item) => item.kind === kind);
  const error = save.error ?? restore.error;
  const contentError = error instanceof ApiError ? error.fields.find((field) => field.key === 'content') : undefined;
  const activeContent = !prompts.data && prompts.isPending ? <StatePanel kind="pending" label={t('common.loading')} />
    : !prompts.data || !document ? <StatePanel kind="error" title={t('web.error.title')} description={prompts.error ? errorText(prompts.error) : t('web.invalid_response')} retry={{ label: t('common.retry'), run: () => { void prompts.refetch(); } }} />
    : <div className="space-y-lg">
      {document.updated_at !== null && timeZone && <p className="text-caption text-text-secondary">{t('web.ai.updated_at', { time: formatDateTime(document.updated_at, locale, timeZone) })}</p>}
      <PromptEditor key={kind} kind={kind} content={document.content} customized={document.customized} requiredKeys={document.required_keys} pending={save.isPending || restore.isPending} onDirtyChange={setDirty} {...(error ? { error: errorText(error) } : {})} {...(contentError ? { fieldError: messageText(contentError.reason) } : {})}
        onSave={async (content) => { restore.reset(); const next = await save.mutateAsync({ kind, content }); return next.content; }}
        onRestore={async () => { save.reset(); const next = await restore.mutateAsync(kind); return next.content; }} />
      {save.isSuccess && !dirty && <p role="status" className="text-body text-positive">{t('web.ai.saved')}</p>}
      {prompts.isError && <AIErrorNotice error={prompts.error} />}
    </div>;
  return <div className="min-w-0 space-y-xl">
    <PageHeader title={t('web.ai.prompts_title')} description={t('web.ai.prompt_next_run')} />
    <Tabs label={t('web.ai.prompts_title')} value={kind} onValueChange={(value) => {
      if (value !== 'analysis' && value !== 'title_screen' && value !== 'profile' && value !== 'market' || value === kind) return;
      if (dirty) setPendingKind(value); else { setKind(value); save.reset(); restore.reset(); }
    }} items={promptKinds.map((value) => ({ value, label: t(promptLabels[value]), ...(value === kind ? { content: activeContent } : {}) }))} />
    <ConfirmDialog open={pendingKind !== null} onOpenChange={(open) => { if (!open) setPendingKind(null); }} title={t('web.ai.discard_title')} description={t('web.ai.discard_description')} confirmLabel={t('web.ai.discard_confirm')} cancelLabel={t('common.cancel')} onConfirm={() => {
      if (pendingKind) { setKind(pendingKind); setDirty(false); setPendingKind(null); save.reset(); restore.reset(); }
    }} />
  </div>;
}
