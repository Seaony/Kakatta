import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiData, useApi, useApiError, useMessageText } from '../../api/hooks';
import { ApiError } from '../../api/errors';
import { apiKey } from '../../api/keys';
import type { Schema } from '../../api/types';
import { useUnsavedChanges } from '../../app/unsaved-changes';
import { useAuthentication } from '../../app/auth-state';
import { useTranslation } from '../../i18n';
import { formatMoney } from '../../i18n/format';
import { Button, Checkbox, ConfirmDialog, FormSection, InlineMessage, RadioGroup, SelectField, Skeleton, StatePanel, TextAreaField, TextField } from '../../components/ui';
import { TaskChannelPicker } from '../notifications';
import { ProfileEditor } from './profile-editor';
import { ProfileGeneration } from './generation';
import { TaskReanalysis } from './reanalysis';
import { OverrideFields, RegionFields } from './fields';
import { useTaskAccounts } from './queries';
import { changedTaskPatch, draftFromFields, initialProfile, moneyInput, parseMoney, prepareTask, profileDraft, searchChanged, suggestedSearchPrice, type DraftIssue, type TaskDraft } from './model';

export type TaskEditorProps = {
  initialTask?: Schema<'Task'>;
  initialFields?: Schema<'TaskFields'>;
  initialKeyword?: string;
  initialTemplate?: 'ipad' | 'macbook' | 'keyword';
  onSaved(result: Schema<'TaskSaveResult'>): void;
  onCancel?: () => void;
  embedded?: boolean;
};

const validationKeys = {
  required: 'tasks.validation.required', name_length: 'tasks.validation.name_length', keyword_length: 'tasks.validation.keyword_length',
  money: 'tasks.validation.money', range: 'tasks.validation.range', invalid_range: 'settings.invalid_range', positive_integer: 'tasks.validation.positive_integer',
  profile: 'tasks.validation.profile', account: 'tasks.validation.account', option: 'tasks.validation.option',
} as const;
const publishKeys = { any: 'tasks.publish.any', day1: 'tasks.publish.day1', day3: 'tasks.publish.day3', day7: 'tasks.publish.day7', day14: 'tasks.publish.day14' } as const;
const notifyKeys = { steal: 'tasks.policy.steal', good: 'tasks.policy.good', fair: 'tasks.policy.fair', all_new: 'tasks.policy.all_new', none: 'tasks.policy.none', recommended: 'tasks.policy.recommended' } as const;
const kindKeys = { sale: 'tasks.kind.sale', wanted: 'tasks.kind.wanted', accessory: 'tasks.kind.accessory', recycle: 'tasks.kind.recycle', rental: 'tasks.kind.rental', repair: 'tasks.kind.repair', box: 'tasks.kind.box', multi: 'tasks.kind.multi', broken: 'tasks.kind.broken', other: 'tasks.kind.other' } as const;

export function TaskEditor(props: TaskEditorProps) {
  const api = useApi(), errorText = useApiError();
  const { t } = useTranslation();
  const defaults = useQuery({ queryKey: apiKey('tasks', 'defaults'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/tasks/defaults', { signal })), enabled: !props.initialTask && !props.initialFields });
  const fields = props.initialTask ?? props.initialFields ?? defaults.data;
  if (!fields && defaults.isPending) return <StatePanel kind="pending" label={t('common.loading')}><Skeleton className="h-xxl" /><Skeleton className="h-xxl" /><Skeleton className="h-xxl" /></StatePanel>;
  if (!fields) return <StatePanel kind="error" title={t('tasks.list.load_failed')} description={errorText(defaults.error)} retry={{ label: t('common.retry'), run: () => { void defaults.refetch(); } }} />;
  return <TaskForm {...props} fields={fields} />;
}

function TaskForm({ fields, initialTask, initialKeyword, initialTemplate, onSaved, onCancel, embedded = false }: TaskEditorProps & { fields: Schema<'TaskFields'> }) {
  const api = useApi(), queryClient = useQueryClient(), errorText = useApiError(), messageText = useMessageText();
  const authenticated = useAuthentication()?.authenticated !== false;
  const { t, locale } = useTranslation();
  const [base, setBase] = useState(fields);
  const applyTemplate = (current: TaskDraft, template: 'ipad' | 'macbook' | 'keyword'): TaskDraft => ({ ...current, keyword: template === 'ipad' ? 'iPad mini' : template === 'macbook' ? 'MacBook Pro' : '', requirement: current.requirement || (template === 'ipad' ? t('tasks.templates.ipad_requirement') : template === 'macbook' ? t('tasks.templates.macbook_requirement') : ''), ...(template === 'keyword' ? { judge_mode: 'keyword', notify_policy: 'recommended' } : {}) });
  const [baselineDraft, setBaselineDraft] = useState(() => { const current = draftFromFields(fields, initialKeyword); return initialTemplate ? applyTemplate(current, initialTemplate) : current; });
  const [draft, setDraft] = useState<TaskDraft>(baselineDraft);
  const [issues, setIssues] = useState<DraftIssue[]>([]);
  const [clearProfileOpen, setClearProfileOpen] = useState(false);
  const [discardOpen, setDiscardOpen] = useState(false);
  const [keptRequirement, setKeptRequirement] = useState(fields.requirement);
  const [saved, setSaved] = useState<Schema<'TaskSaveResult'> | null>(null);
  const [generating, setGenerating] = useState(false);
  const [generationIssues, setGenerationIssues] = useState<Schema<'FieldIssue'>[]>([]);
  const [persistedTaskID, setPersistedTaskID] = useState(initialTask?.id ?? null);
  const [recoveredAsNew, setRecoveredAsNew] = useState(false);
  const summaryRef = useRef<HTMLDivElement>(null);
  const formRef = useRef<HTMLFormElement>(null);
  const saveOperation = useRef<AbortController | null>(null);
  const dictionary = useQuery({ queryKey: apiKey('tasks', 'risk-dictionary'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/risk-dictionary', { signal })), staleTime: Infinity });
  const settings = useQuery({ queryKey: apiKey('settings'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings', { signal })), staleTime: 30_000 });
  const definitions = useQuery({ queryKey: apiKey('setting-definitions'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings/definitions', { signal })), staleTime: Infinity });
  const accounts = useTaskAccounts();
  const save = useMutation({ mutationFn: async ({ body, controller }: { body: Schema<'TaskCreate'>; controller: AbortController }) => {
    controller.signal.throwIfAborted();
    return persistedTaskID !== null
      ? apiData(await api.PATCH('/api/v1/tasks/{task_id}', { params: { path: { task_id: persistedTaskID } }, body: changedTaskPatch(base, body), signal: controller.signal }))
      : apiData(await api.POST('/api/v1/tasks', { body, signal: controller.signal }));
    },
    onSuccess(result, { controller }) {
      if (saveOperation.current !== controller || controller.signal.aborted) return;
      const next = draftFromFields(result.task);
      setBase(result.task); setDraft(next); setBaselineDraft(next); setKeptRequirement(result.task.requirement); setSaved(result); setIssues([]); setClearProfileOpen(false); setPersistedTaskID(result.task.id);
      for (const resource of ['tasks', 'findings', 'overview']) void queryClient.invalidateQueries({ queryKey: apiKey(resource) });
      onSaved(result);
    },
    onSettled(_result, _error, { controller }) { if (saveOperation.current === controller) saveOperation.current = null; },
  });
  useEffect(() => () => { saveOperation.current?.abort(); saveOperation.current = null; }, []);
  useEffect(() => {
    if (!authenticated && saveOperation.current) { saveOperation.current.abort(); saveOperation.current = null; save.reset(); }
  }, [authenticated, save.reset]);
  const dirty = JSON.stringify(draft) !== JSON.stringify(baselineDraft);
  useUnsavedChanges(dirty || generating);
  const errorFor = (key: string): string | undefined => {
    const local = issues.find((issue) => issue.field === key);
    if (local) return t(validationKeys[local.reason]);
    const field = (save.error instanceof ApiError ? save.error.fields : generationIssues).find((issue) => issue.key === key);
    return field ? messageText(field.reason) : undefined;
  };
  useEffect(() => {
    if (!issues.length && !save.error && !generationIssues.length) return;
    const invalid = formRef.current?.querySelector<HTMLElement>('input[aria-invalid="true"],textarea[aria-invalid="true"],select[aria-invalid="true"],[role="radiogroup"][aria-invalid="true"] button');
    (invalid ?? summaryRef.current)?.focus();
  }, [issues, save.error, generationIssues]);
  const fieldError = (key: string) => { const error = errorFor(key); return error ? { error } : {}; };
  const update = <K extends keyof TaskDraft>(key: K, value: TaskDraft[K]) => { setDraft((current) => ({ ...current, [key]: value })); };
  const submit = async (confirmed = false) => {
    if (saveOperation.current || save.isPending || generating || !authenticated) return;
    const prepared = prepareTask(draft);
    const nextIssues = persistedTaskID !== null && !draft.name.trim() ? [...prepared.issues, { field: 'name', reason: 'required' as const }] : prepared.issues;
    setIssues(nextIssues);
    if (!prepared.body || nextIssues.length) return;
    const hasProfile = draft.profile.specs.length + draft.profile.musts.length + draft.profile.bonuses.length + draft.profile.risks.length > 0;
    if (draft.judge_mode === 'keyword' && hasProfile && !confirmed) { setClearProfileOpen(true); return; }
    const controller = new AbortController(); saveOperation.current = controller;
    try { await save.mutateAsync({ body: prepared.body, controller }); } catch { /* 错误留在表单，由统一提示和字段定位接管。 */ }
  };
  const onSubmit = (event: FormEvent) => { event.preventDefault(); void submit(); };
  const accountOptions = [{ value: '', label: t('tasks.account.auto') }, ...(accounts.data ?? []).map((account) => ({ value: String(account.id), label: `${account.name} · ${t(`accounts.state.${account.state}`)}` }))];
  if (draft.account_pinned && draft.account_id !== null && !accounts.data?.some((account) => account.id === draft.account_id)) accountOptions.push({ value: String(draft.account_id), label: t('tasks.account.unavailable', { id: draft.account_id }) });
  const availablePolicies = draft.judge_mode === 'ai' ? ['steal', 'good', 'fair', 'all_new', 'none'] as const : ['recommended', 'all_new', 'none'] as const;
  const expandedPrice = suggestedSearchPrice(parseMoney(draft.budget_max));
  const dependencyError = dictionary.error ?? accounts.error ?? definitions.error ?? settings.error;

  return <div className={embedded ? 'min-w-0' : 'min-w-0 max-w-5xl'}>
    <form ref={formRef} onSubmit={onSubmit} noValidate className="min-w-0 space-y-xxl" onKeyDown={(event) => { if (event.key === 'Enter' && event.nativeEvent.isComposing) event.preventDefault(); }}>
      {(issues.length > 0 || save.error) && <div ref={summaryRef} tabIndex={-1} className="space-y-sm" role="alert"><InlineMessage tone="danger" title={t('tasks.validation.title')}>
        {save.error && <p>{errorText(save.error)}</p>}{save.error instanceof ApiError && save.error.requestID && <p>{t('web.request_id', { request_id: save.error.requestID })}</p>}
        {issues.map((issue) => <p key={issue.field}>{t(validationKeys[issue.reason])}</p>)}
        {save.error instanceof ApiError && save.error.fields.map((field) => <p key={field.key}>{messageText(field.reason)}</p>)}
        {save.error instanceof ApiError && save.error.status === 0 && <p>{t('tasks.save.unknown_outcome')}</p>}
        {save.error instanceof ApiError && save.error.status === 404 && persistedTaskID !== null && <Button onClick={() => { setPersistedTaskID(null); setRecoveredAsNew(true); save.reset(); setDraft((current) => ({ ...current, account_id: current.account_pinned ? current.account_id : null })); }}>{t('tasks.copy')}</Button>}
      </InlineMessage></div>}
      {recoveredAsNew && <InlineMessage title={t('tasks.new')}>{t('tasks.editor.description')}</InlineMessage>}
      {dependencyError && <InlineMessage tone="warning" action={<Button onClick={() => { void dictionary.refetch(); void accounts.refetch(); void definitions.refetch(); void settings.refetch(); }}>{t('common.retry')}</Button>}>{errorText(dependencyError)}</InlineMessage>}
      <fieldset disabled={save.isPending || !authenticated} className="min-w-0 space-y-xxl">
        {persistedTaskID === null && <FormSection title={t('tasks.templates.title')} description={t('tasks.templates.description')}><TaskTemplateButtons disabled={generating} onSelect={(template) => setDraft((current) => applyTemplate(current, template))} /></FormSection>}
        <FormSection title={t('tasks.section.basic')}><div className="grid min-w-0 gap-lg md:grid-cols-2"><TextField label={t('tasks.field.name')} hint={t('tasks.field.name_hint')} value={draft.name} onChange={(event) => update('name', event.target.value)} {...fieldError('name')} /><TextField label={t('tasks.field.keyword')} required disabled={generating} value={draft.keyword} onChange={(event) => update('keyword', event.target.value)} {...fieldError('keyword')} /></div>
          <RadioGroup label={t('tasks.field.judge_mode')} value={draft.judge_mode} disabled={generating} orientation="horizontal" options={[{ value: 'ai', label: t('tasks.mode.ai') }, { value: 'keyword', label: t('tasks.mode.keyword') }]} onValueChange={(value) => {
            if (value !== 'ai' && value !== 'keyword') return;
            setDraft((current) => ({ ...current, judge_mode: value, profile: value === 'ai' && current.profile.specs.length + current.profile.musts.length + current.profile.risks.length === 0 ? initialProfile(dictionary.data ?? []) : current.profile, notify_policy: value === 'keyword' && !['recommended', 'all_new', 'none'].includes(current.notify_policy) ? 'recommended' : value === 'ai' && current.notify_policy === 'recommended' ? 'good' : current.notify_policy }));
          }} />
        </FormSection>
        <FormSection title={t('tasks.section.search')}><div className="grid min-w-0 gap-lg sm:grid-cols-2"><TextField label={t('tasks.field.min_price')} inputMode="decimal" value={draft.min_price} onChange={(event) => update('min_price', event.target.value)} {...fieldError('min_price')} /><TextField label={t('tasks.field.max_price')} inputMode="decimal" value={draft.max_price} onChange={(event) => update('max_price', event.target.value)} {...fieldError('max_price')} /></div><div className="flex flex-wrap gap-lg"><Checkbox label={t('tasks.field.personal_only')} checked={draft.personal_only} onCheckedChange={(value) => update('personal_only', value)} /><Checkbox label={t('tasks.field.free_shipping')} checked={draft.free_shipping_only} onCheckedChange={(value) => update('free_shipping_only', value)} /></div>
          <SelectField label={t('tasks.field.publish_window')} value={draft.publish_window} options={Object.entries(publishKeys).map(([value, key]) => ({ value, label: t(key) }))} onValueChange={(value) => update('publish_window', value)} {...fieldError('publish_window')} />
          <RegionFields draft={draft} onChange={setDraft} disabled={save.isPending} errorFor={errorFor} />
          <TextField label={t('tasks.field.max_pages')} inputMode="numeric" value={draft.max_pages} onChange={(event) => update('max_pages', event.target.value)} {...fieldError('max_pages')} />{Number(draft.max_pages) > 3 && <InlineMessage tone="warning">{t('tasks.pages_warning')}</InlineMessage>}
        </FormSection>
        <FormSection title={t('tasks.section.judgment')}>
          {draft.judge_mode === 'ai' ? <><TextAreaField label={t('tasks.field.requirement')} value={draft.requirement} disabled={generating} onChange={(event) => update('requirement', event.target.value)} {...fieldError('requirement')} />
            {draft.requirement !== keptRequirement && <InlineMessage tone="info" action={<Button onClick={() => setKeptRequirement(draft.requirement)}>{t('tasks.profile.keep_current')}</Button>}>{t('tasks.profile.requirement_changed')}</InlineMessage>}
            {dictionary.data ? <ProfileEditor value={draft.profile} onChange={(value) => update('profile', value)} dictionary={dictionary.data} disabled={save.isPending || generating} {...fieldError('profile')} generation={<ProfileGeneration {...(persistedTaskID !== null ? { taskID: persistedTaskID } : {})} keyword={draft.keyword} requirement={draft.requirement} profile={draft.profile} disabled={save.isPending} onBusyChange={(busy) => { setGenerating(busy); if (busy) { save.reset(); setIssues([]); } }} onFieldIssues={setGenerationIssues} onGenerated={(result, requirement) => { setDraft((current) => ({ ...current, profile: profileDraft(result.profile) })); setKeptRequirement(requirement); }} onApply={setDraft} />} /> : dictionary.isPending ? <StatePanel kind="pending" label={t('common.loading')}><Skeleton /><Skeleton /></StatePanel> : <StatePanel kind="error" title={t('web.error.title')} description={errorText(dictionary.error)} retry={{ label: t('common.retry'), run: () => { void dictionary.refetch(); } }} />}
            <details className="min-w-0 border-t border-border pt-lg"><summary className="min-h-control cursor-pointer text-body font-medium">{t('tasks.profile.allowed_kinds')}</summary><div className="grid min-w-0 gap-sm sm:grid-cols-2">{Object.entries(kindKeys).map(([kind, key]) => <Checkbox key={kind} label={t(key)} checked={draft.profile.allowed_kinds.some((allowed) => allowed === kind)} onCheckedChange={(checked) => {
              const selected = Object.keys(kindKeys).find((entry): entry is Schema<'AnalysisKind'> => entry === kind);
              if (selected) update('profile', { ...draft.profile, allowed_kinds: checked ? [...draft.profile.allowed_kinds, selected] : draft.profile.allowed_kinds.filter((item) => item !== selected) });
            }} />)}</div></details>
          </> : <TextAreaField label={t('tasks.field.match_keywords')} hint={t('tasks.field.words_hint')} value={draft.match_keywords_text} onChange={(event) => update('match_keywords_text', event.target.value)} {...fieldError('match_keywords')} />}
          <TextAreaField label={t('tasks.field.exclude_keywords')} hint={t('tasks.field.words_hint')} value={draft.exclude_keywords_text} onChange={(event) => update('exclude_keywords_text', event.target.value)} {...fieldError('exclude_keywords')} />
        </FormSection>
        <FormSection title={t('tasks.section.budget')} description={t('tasks.budget.explanation')}><div className="grid min-w-0 gap-lg sm:grid-cols-2"><TextField label={t('tasks.field.budget_min')} inputMode="decimal" value={draft.budget_min} onChange={(event) => update('budget_min', event.target.value)} {...fieldError('budget_min')} /><TextField label={t('tasks.field.budget_max')} inputMode="decimal" value={draft.budget_max} onChange={(event) => update('budget_max', event.target.value)} {...fieldError('budget_max')} /></div>{expandedPrice !== null && <InlineMessage action={<Button onClick={() => update('max_price', moneyInput(expandedPrice))}>{t('tasks.suggestions.apply')}</Button>}>{t('tasks.budget.search_price_suggestion', { price: formatMoney(expandedPrice, locale) })}</InlineMessage>}</FormSection>
        <FormSection title={t('tasks.section.schedule')}><Checkbox label={t('tasks.enabled')} checked={draft.enabled} onCheckedChange={(value) => update('enabled', value)} /><TextField label={t('tasks.field.interval')} inputMode="numeric" value={draft.interval_minutes} hint={t('tasks.search_delay')} onChange={(event) => update('interval_minutes', event.target.value)} {...fieldError('interval_minutes')} />{(Number(draft.interval_minutes) < 30 || Number(draft.interval_minutes) > 120) && <InlineMessage tone="warning">{t('tasks.interval_warning')}</InlineMessage>}<Checkbox label={t('tasks.field.analyze_first')} hint={t('tasks.first_run_analysis')} checked={draft.analyze_on_first_run} onCheckedChange={(value) => update('analyze_on_first_run', value)} /></FormSection>
        <FormSection title={t('tasks.section.notifications')}><SelectField label={t('tasks.field.notify_policy')} value={draft.notify_policy} options={availablePolicies.map((policy) => ({ value: policy, label: t(notifyKeys[policy]) }))} onValueChange={(value) => update('notify_policy', value)} {...fieldError('notify_policy')} /><Checkbox label={t('tasks.field.price_drop')} checked={draft.notify_price_drop} onCheckedChange={(value) => update('notify_price_drop', value)} /><TaskChannelPicker value={draft.channel_ids} onChange={(value) => update('channel_ids', value)} disabled={save.isPending} {...fieldError('channel_ids')} /></FormSection>
        <FormSection title={t('tasks.section.advanced')}><SelectField label={t('tasks.field.account')} value={draft.account_pinned ? String(draft.account_id ?? '') : ''} options={accountOptions} onValueChange={(value) => { const accountID = value ? Number(value) : null; setDraft((current) => ({ ...current, account_id: accountID, account_pinned: accountID !== null })); }} {...fieldError('account_id')} />{accounts.isSuccess && accounts.data.length === 0 && <InlineMessage tone="warning">{t('tasks.account.none')}</InlineMessage>}
          {definitions.data && settings.data ? <OverrideFields value={draft.crawl_overrides} onChange={(value) => update('crawl_overrides', value)} definitions={definitions.data} globals={settings.data.values} disabled={save.isPending} errorFor={errorFor} /> : <p className="text-body text-text-secondary">{t('tasks.overrides.loading')}</p>}
        </FormSection>
      </fieldset>
      {persistedTaskID !== null && searchChanged(base, draft) && <InlineMessage tone="warning">{t('tasks.search_changed')}</InlineMessage>}
      <footer className="flex flex-wrap items-center gap-md border-t border-border py-xl"><Button type="submit" variant="primary" busy={save.isPending} disabled={generating || !authenticated} busyLabel={t('tasks.saving')}>{t('tasks.save')}</Button>{onCancel && <Button disabled={save.isPending || generating} onClick={() => { if (dirty) setDiscardOpen(true); else onCancel(); }}>{t('common.cancel')}</Button>}</footer>
    </form>
    {saved && <TaskSaveFeedback result={saved} />}
    <ConfirmDialog open={clearProfileOpen} onOpenChange={setClearProfileOpen} title={t('tasks.mode.clear_title')} description={t('tasks.mode.clear_profile_confirmation')} confirmLabel={t('tasks.save')} cancelLabel={t('common.cancel')} destructive pending={save.isPending} {...(save.error ? { error: errorText(save.error) } : {})} onConfirm={() => { void submit(true); }} />
    <ConfirmDialog open={discardOpen} onOpenChange={setDiscardOpen} title={t('tasks.unsaved.title')} description={t('tasks.unsaved.description')} confirmLabel={t('tasks.discard')} cancelLabel={t('common.cancel')} destructive onConfirm={() => { setDiscardOpen(false); onCancel?.(); }} />
  </div>;
}

export function TaskTemplateButtons({ disabled = false, onSelect }: { disabled?: boolean; onSelect(template: 'ipad' | 'macbook' | 'keyword'): void }) {
  const { t } = useTranslation();
  return <div className="flex flex-wrap gap-sm">{(['ipad', 'macbook', 'keyword'] as const).map((template) => <Button key={template} disabled={disabled} onClick={() => onSelect(template)}>{t(template === 'ipad' ? 'tasks.templates.ipad' : template === 'macbook' ? 'tasks.templates.macbook' : 'tasks.templates.keyword')}</Button>)}</div>;
}

export function TaskSaveFeedback({ result }: { result: Schema<'TaskSaveResult'> }) {
  const api = useApi(), queryClient = useQueryClient(), errorText = useApiError(), messageText = useMessageText();
  const { t } = useTranslation();
  const [kept, setKept] = useState(false);
  const apply = useMutation({ mutationFn: async () => apiData(await api.POST('/api/v1/tasks/{task_id}/apply-exclusions', { params: { path: { task_id: result.task.id } }, body: { added_exclude_words: result.effects.added_exclude_words } })), onSuccess() { for (const resource of ['tasks', 'findings', 'overview']) void queryClient.invalidateQueries({ queryKey: apiKey(resource) }); } });
  const effects = result.effects;
  return <section className="min-w-0 space-y-lg" aria-label={t('tasks.saved')}>
    <InlineMessage tone="positive" title={t('tasks.saved')}><p>{t('tasks.effects.rescored', { count: effects.rescored_count })}</p>{effects.search_reset && <p>{t('tasks.search_changed')}</p>}</InlineMessage>
    {result.warnings.length > 0 && <InlineMessage tone="warning">{result.warnings.map((warning, index) => <p key={`${warning.key}-${index}`}>{messageText(warning.reason)}</p>)}</InlineMessage>}
    {(effects.needs_reanalysis_ids.length > 0 || effects.title_rejected_ids.length > 0) && <TaskReanalysis key={`${result.task.id}-${result.task.updated_at ?? ''}-${effects.needs_reanalysis_ids.join(',')}-${effects.title_rejected_ids.join(',')}`} taskID={result.task.id} effects={effects} />}
    {effects.existing_exclude_match_count > 0 && !kept && !apply.isSuccess && <InlineMessage tone="warning" title={t('tasks.excludes.hide_existing', { count: effects.existing_exclude_match_count })} action={<div className="flex flex-wrap gap-sm"><Button busy={apply.isPending} onClick={() => apply.mutate()}>{t('tasks.effects.hide')}</Button><Button disabled={apply.isPending} onClick={() => setKept(true)}>{t('tasks.effects.keep')}</Button></div>}><p>{t('tasks.effects.exclusion_hint')}</p>{apply.error && <p role="alert">{errorText(apply.error)}</p>}</InlineMessage>}
    {apply.isSuccess && <InlineMessage tone="positive">{t('tasks.effects.exclusions_applied')}</InlineMessage>}
  </section>;
}
