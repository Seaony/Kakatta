import { useEffect, useMemo, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { useUnsavedChanges } from '../../app/unsaved-changes';
import { apiData, useApi, useApiError, useMessageText } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import { ApiError, isCancellation } from '../../api/errors';
import { selectLocale, useTranslation } from '../../i18n';
import { useTheme } from '../../app/theme';
import { Button } from '../../components/ui/button';
import { ConfirmDialog } from '../../components/ui/dialog';
import { Skeleton } from '../../components/ui/skeleton';
import { StatePanel } from '../../components/ui/state-panel';
import { FormSection, InlineMessage, PageHeader } from '../../components/ui';
import { buildPatch, generalGroups, limitGroups, makeDraft, settingOwner } from './model';
import type { Draft, Drafts, FieldIssue } from './model';
import { SettingField, fieldID } from './setting-field';
import { Maintenance } from './maintenance';
import { BlockedSellers } from './blocked-sellers';
import { useCancellableAction } from './use-cancellable-action';

export function SettingsPage({ mode }: { mode: 'general' | 'limits' }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const { t, setLocale } = useTranslation();
  const { setTheme } = useTheme();
  const errorText = useApiError();
  const text = useMessageText();
  const [drafts, setDrafts] = useState<Drafts>({});
  const [bases, setBases] = useState<Record<string, string>>({});
  const [fields, setFields] = useState<FieldIssue[]>([]);
  const [saveError, setSaveError] = useState<unknown>(null);
  const [saved, setSaved] = useState(false);
  const [pending, setPending] = useState(false);
  const [maintenancePending, setMaintenancePending] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const [discard, setDiscard] = useState(false);
  const action = useCancellableAction(() => { setPending(false); setUncertain(true); });
  useEffect(() => { if (!action.enabled) setDiscard(false); }, [action.enabled]);
  const settings = useQuery({ queryKey: apiKey('settings'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings', { signal })), enabled: action.enabled });
  const definitions = useQuery({ queryKey: apiKey('setting-definitions'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings/definitions', { signal })), enabled: action.enabled });
  const readError = settings.error ?? definitions.error;
  const readRequestID = readError instanceof ApiError ? readError.requestID : '';

  const owned = useMemo(() => definitions.data?.filter((definition) => settingOwner(definition.key) === mode) ?? [], [definitions.data, mode]);
  const snapshot = settings.data;
  const patch = useMemo(() => snapshot ? buildPatch(owned, drafts, snapshot) : { values: {}, fields: [] }, [owned, drafts, snapshot]);
  const dirty = Object.keys(patch.values).length > 0 || patch.fields.length > 0;
  useUnsavedChanges(dirty || pending || maintenancePending);
  const groups = mode === 'general' ? generalGroups : limitGroups;
  const requiredKeys = groups.flatMap((group) => [...group]);
  const incomplete = snapshot && definitions.data && requiredKeys.some((key) => {
    const definition = definitions.data.find((item) => item.key === key);
    return !definition || !Object.hasOwn(snapshot.values, key) || !Object.hasOwn(snapshot.sources, key) || !makeDraft(definition, snapshot.values[key]);
  });
  const conflicts = snapshot ? Object.keys(drafts).filter((key) => bases[key] !== undefined && bases[key] !== JSON.stringify(snapshot.values[key])) : [];

  async function refresh() {
    if (!action.isActive()) return;
    const results = await Promise.all([settings.refetch(), definitions.refetch()]);
    if (action.isActive() && results.every((result) => result.isSuccess)) setUncertain(false);
  }

  function change(key: string, draft: Draft) {
    if (!snapshot || pending || maintenancePending || !action.isActive()) return;
    setBases((current) => Object.hasOwn(current, key) ? current : { ...current, [key]: JSON.stringify(snapshot.values[key]) });
    setDrafts((current) => ({ ...current, [key]: draft }));
    setFields((current) => current.filter((field) => field.key !== key));
    setSaved(false);
    setSaveError(null);
  }

  function reset() {
    setDrafts({}); setBases({}); setFields([]); setSaveError(null); setSaved(false); setDiscard(false);
  }

  async function save() {
    if (!snapshot || pending || maintenancePending || incomplete || uncertain || !action.isActive()) return;
    setFields(patch.fields);
    setSaved(false);
    setSaveError(null);
    if (patch.fields.length) { document.getElementById(fieldID(patch.fields[0]?.key ?? ''))?.focus(); return; }
    if (!Object.keys(patch.values).length) return;
    const abort = action.start();
    if (!abort) return;
    setPending(true);
    try {
      const result = apiData(await api.PATCH('/api/v1/settings', { body: { values: patch.values }, signal: abort.signal }));
      if (!action.isCurrent(abort)) return;
      const language = result.values['general.language'];
      const appearance = result.values['general.appearance'];
      if (language === 'system' || language === 'zh-CN' || language === 'en') await setLocale(selectLocale(language));
      if (!action.isCurrent(abort)) return;
      queryClient.setQueryData(apiKey('settings'), result);
      if (appearance === 'system' || appearance === 'light' || appearance === 'dark') setTheme(appearance);
      reset(); setSaved(true); setUncertain(false);
      void queryClient.invalidateQueries({ queryKey: apiKey('me') });
      void queryClient.invalidateQueries({ queryKey: apiKey('tasks') });
      void queryClient.invalidateQueries({ queryKey: apiKey('system-status') });
    } catch (error) {
      if (action.isCurrent(abort)) {
        if (isCancellation(error) || abort.signal.aborted) setUncertain(true);
        else {
          setSaveError(error);
          if (error instanceof ApiError) setFields(error.fields);
          if (!(error instanceof ApiError) || error.status === 0 || error.status >= 500) setUncertain(true);
        }
      }
    } finally {
      if (action.finish(abort)) setPending(false);
    }
  }

  const title = t(mode === 'general' ? 'web.settings.general_title' : 'web.settings.limits_title');
  if (!snapshot || !definitions.data) {
    const error = settings.error ?? definitions.error;
    return <div className="space-y-xl"><PageHeader title={title} />{error
      ? <StatePanel kind="error" title={t('web.settings.load_failed')} description={errorText(error)} retry={{ label: t('common.retry'), run: () => { void refresh(); } }} />
      : <StatePanel kind="pending" label={t('web.settings.loading')}><Skeleton className="h-[4rem]" /><Skeleton className="h-[10rem]" /><Skeleton className="h-[10rem]" /></StatePanel>}{error instanceof ApiError && error.requestID && <p className="break-all text-caption text-text-secondary">{t('web.request_id', { request_id: error.requestID })}</p>}</div>;
  }

  const generalTitles = [t('web.settings.display_section'), t('web.settings.schedule_section'), t('web.settings.links_section'), t('web.settings.retention_section'), t('web.settings.cache_section')];
  const limitTitles = [t('web.settings.crawl_frequency_section'), t('web.settings.crawl_browsing_section'), t('web.settings.crawl_protection_section')];
  const timezone = snapshot.values['general.timezone'];
  const quietDefinition = definitions.data.find((definition) => definition.key === 'crawl.quiet_hours');
  const quiet = quietDefinition ? makeDraft(quietDefinition, snapshot.values['crawl.quiet_hours']) : null;
  const visibleFields = [...fields, ...patch.fields].filter((field, index, all) => all.findIndex((item) => item.key === field.key && item.reason.code === field.reason.code) === index);

  return <div className="min-w-0 space-y-xxl">
    <PageHeader title={title} description={t(mode === 'general' ? 'web.settings.general_description' : 'web.settings.limits_description')} actions={<Button onClick={() => { void refresh(); }} busy={settings.isFetching || definitions.isFetching} disabled={pending || maintenancePending || !action.enabled}>{t('web.settings.refresh')}</Button>} />
    {(settings.isError || definitions.isError) && <InlineMessage tone="warning" action={<Button onClick={() => { void refresh(); }}>{t('common.retry')}</Button>}><p>{t('web.settings.refresh_failed')}</p>{readRequestID && <p className="mt-sm break-all text-caption">{t('web.request_id', { request_id: readRequestID })}</p>}</InlineMessage>}
    {incomplete && <InlineMessage tone="danger">{t('web.settings.incomplete')}</InlineMessage>}
    {conflicts.length > 0 && <InlineMessage tone="warning">{t('web.settings.changed_elsewhere')}</InlineMessage>}
    {uncertain && <InlineMessage tone="warning" action={<Button onClick={() => { void refresh(); }}>{t('web.settings.refresh')}</Button>}>{t('web.settings.save_uncertain')}</InlineMessage>}
    {saved && <InlineMessage tone="positive">{t('web.settings.saved')}</InlineMessage>}
    {(saveError !== null || visibleFields.length > 0) && <div role="alert" tabIndex={-1} className="space-y-sm rounded-control bg-danger-fill p-lg text-danger">
      <p>{saveError ? errorText(saveError) : t('settings.invalid')}</p>
      {visibleFields.length > 0 && <ul className="space-y-sm">{visibleFields.map((field) => <li key={`${field.key}-${field.reason.code}`}><Button variant="plain" onClick={() => document.getElementById(fieldID(field.key))?.focus()}>{text(owned.find((definition) => definition.key === field.key)?.label ?? field.reason)}: {text(field.reason)}</Button></li>)}</ul>}
      {saveError instanceof ApiError && saveError.requestID && <p className="break-all text-caption">{t('web.request_id', { request_id: saveError.requestID })}</p>}
    </div>}
    {mode === 'limits' && <InlineMessage tone="warning">{t('web.settings.crawl_safety_notice')}</InlineMessage>}
    <form onSubmit={(event) => { event.preventDefault(); void save(); }} onKeyDown={(event) => { if (event.key === 'Enter' && event.nativeEvent.isComposing) event.preventDefault(); }} className="space-y-xxl">
      {groups.map((keys, index) => <FormSection key={keys[0]} title={(mode === 'general' ? generalTitles : limitTitles)[index] ?? title}>
        {mode === 'general' && index === 1 && <InlineMessage tone="info"><p>{t('web.settings.timezone_quota_notice')}</p><p className="mt-sm">{t('web.settings.quiet_hours_timezone', { timezone: typeof timezone === 'string' ? timezone : '' })}</p></InlineMessage>}
        {mode === 'general' && index === 3 && <p className="max-w-prose text-body text-text-secondary">{t('web.settings.retention_notice')}</p>}
        {keys.map((key) => {
          const definition = owned.find((item) => item.key === key);
          const draft = drafts[key] ?? (definition ? makeDraft(definition, snapshot.values[key]) : null);
          return definition && draft ? <SettingField key={key} definition={definition} draft={draft} source={snapshot.sources[key]} errors={visibleFields.filter((field) => field.key === key).map((field) => field.reason)} warnings={drafts[key] ? [] : snapshot.warnings.filter((field) => field.key === key).map((field) => field.reason)} disabled={pending || maintenancePending || !action.enabled || Boolean(incomplete)} onChange={(value) => change(key, value)} /> : null;
        })}
      </FormSection>)}
      {mode === 'limits' && <FormSection title={t('web.settings.quiet_summary_title')} description={t('web.settings.task_override_notice')}>
        <p className="max-w-prose text-body text-text-secondary">{t('web.settings.account_limits_notice')}</p>
        {quiet?.kind === 'quiet_hours' && <p className="text-body tabular-nums">{quiet.enabled && quiet.start !== quiet.end ? `${quiet.start}–${quiet.end}` : t('web.settings.quiet_all_day')} {typeof timezone === 'string' ? timezone : ''}</p>}
        <ul className="grid gap-sm text-body text-text-label sm:grid-cols-2">{definitions.data.filter((definition) => definition.task_override).map((definition) => <li key={definition.key}>{text(definition.label)}</li>)}</ul>
        <Link to="/settings/general" className="inline-flex min-h-control items-center text-accent-text underline underline-offset-4">{t('web.settings.edit_general')}</Link>
      </FormSection>}
      <div className="sticky bottom-0 z-10 flex flex-wrap items-center gap-md border-t border-border bg-surface py-lg">
        <Button type="submit" variant="primary" busy={pending} disabled={!dirty || maintenancePending || !action.enabled || Boolean(incomplete) || uncertain}>{t('web.settings.save')}</Button>
        {pending ? <Button onClick={action.cancel}>{t('web.settings.stop_waiting')}</Button> : <Button onClick={() => setDiscard(true)} disabled={!dirty}>{t('web.settings.discard')}</Button>}
        {dirty && <p className="text-body text-text-secondary">{t('web.settings.not_saved')}</p>}
      </div>
    </form>
    {mode === 'general' && <>
      <Maintenance disabled={pending || !action.enabled} onBusyChange={setMaintenancePending} />
      <BlockedSellers disabled={maintenancePending || !action.enabled} {...(typeof timezone === 'string' ? { timeZone: timezone } : {})} />
      <FormSection title={t('web.settings.related_section')}>
        <nav aria-label={t('web.settings.related_section')} className="flex flex-wrap gap-x-xl gap-y-sm">
          <Link to="/settings/limits" className="inline-flex min-h-control items-center text-accent-text underline underline-offset-4">{t('web.settings.limits_title')}</Link>
          <Link to="/settings/ai" className="inline-flex min-h-control items-center text-accent-text underline underline-offset-4">{t('web.settings.ai_link')}</Link>
          <Link to="/settings/notifications" className="inline-flex min-h-control items-center text-accent-text underline underline-offset-4">{t('web.settings.notifications_link')}</Link>
          <Link to="/settings/security" className="inline-flex min-h-control items-center text-accent-text underline underline-offset-4">{t('web.settings.security_link')}</Link>
          <Link to="/settings/about" className="inline-flex min-h-control items-center text-accent-text underline underline-offset-4">{t('web.settings.about_link')}</Link>
        </nav>
      </FormSection>
    </>}
    <ConfirmDialog open={discard && action.enabled} onOpenChange={setDiscard} title={t('web.settings.discard_title')} description={t('web.settings.discard_description')} confirmLabel={t('web.settings.discard')} cancelLabel={t('web.settings.continue_editing')} onConfirm={() => { action.cancel(); reset(); }} />
  </div>;
}

export function GeneralPage() { return <SettingsPage mode="general" />; }
export function LimitsPage() { return <SettingsPage mode="limits" />; }
