import { useEffect, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { Button, Checkbox, FormSection, InlineMessage, PageHeader, RadioGroup, Skeleton, StatePanel, TextField } from '../../components/ui';
import { apiData, useApi, useApiError, useMessageText } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import { ApiError } from '../../api/errors';
import type { components } from '../../api/schema.gen';
import { useTranslation } from '../../i18n';
import { channelCatalog } from './catalog';
import { useUnsavedChanges } from '../../app/unsaved-changes';
import { useNotificationOperation } from './use-notification-operation';
import { ChannelSetup, useChannelAdapter, useChannels } from './channels';
import type { ChannelAdapter, ChannelRow } from './types';

type Snapshot = components['schemas']['SettingsSnapshot'];
type Definition = components['schemas']['SettingDefinition'];
type RulesDraft = { link: 'mobile' | 'desktop'; maximum: string; all: boolean; ids: number[] };

export function readNotificationRules(snapshot: Snapshot): RulesDraft {
  const link = snapshot.values['notify.link_target']; const maximum = snapshot.values['notify.max_per_run']; const ids = snapshot.values['notify.system_channel_ids'];
  if ((link !== 'mobile' && link !== 'desktop') || typeof maximum !== 'number' || !Number.isSafeInteger(maximum) || maximum < 1 || !Array.isArray(ids) || !ids.every((id): id is number => typeof id === 'number' && Number.isSafeInteger(id) && id > 0)) throw new ApiError(0, { code: 'web.invalid_response', args: {} });
  return { link, maximum: String(maximum), all: ids.length === 0, ids };
}

function RulesForm({ initial, definitions, adapter }: { initial: Snapshot; definitions: Definition[]; adapter: ChannelAdapter }) {
  const api = useApi(); const queryClient = useQueryClient(); const { t } = useTranslation(); const errorText = useApiError(); const messageText = useMessageText();
  const channels = useChannels(adapter);
  const [baseline, setBaseline] = useState(initial); const [draft, setDraft] = useState(() => readNotificationRules(initial));
  const [saving, setSaving] = useState(false); const [error, setError] = useState<unknown>(null); const [saved, setSaved] = useState(false);
  const [invalid, setInvalid] = useState<string | null>(null);
  const dirty = JSON.stringify(draft) !== JSON.stringify(readNotificationRules(baseline));
  useUnsavedChanges(dirty);
  useEffect(() => { if (!dirty && initial !== baseline) { setBaseline(initial); setDraft(readNotificationRules(initial)); } }, [initial, baseline, dirty]);
  const operation = useNotificationOperation();
  useEffect(() => { if (operation.authenticated === false) { setSaving(false); setError(null); setSaved(false); } }, [operation.authenticated]);
  const fields = error instanceof ApiError ? error.fields : [];
  const fieldError = (key: string) => { const found = fields.find((issue) => issue.key === key); return found ? messageText(found.reason) : undefined; };
  const hint = (key: string) => { const definition = definitions.find((item) => item.key === key); return definition ? [messageText(definition.why), messageText(definition.recommendation.text), messageText(definition.risk)].join(' ') : ''; };
  function change(patch: Partial<RulesDraft>) { setDraft((old) => ({ ...old, ...patch })); setSaved(false); }
  async function save() {
    if (saving || operation.authenticated === false) return;
    const maximum = Number(draft.maximum);
    if (!/^\d+$/.test(draft.maximum) || !Number.isSafeInteger(maximum) || maximum < 1) { setInvalid('notify.max_per_run'); return; }
    if (!draft.all && !draft.ids.length) { setInvalid('notify.system_channel_ids'); return; }
    setInvalid(null); setError(null); setSaving(true); setSaved(false);
    const controller = operation.begin(); if (!controller) { setSaving(false); return; }
    const values: components['schemas']['SettingValues'] = {};
    if (draft.link !== baseline.values['notify.link_target']) values['notify.link_target'] = draft.link;
    if (maximum !== baseline.values['notify.max_per_run']) values['notify.max_per_run'] = maximum;
    const ids = draft.all ? [] : draft.ids;
    if (JSON.stringify(ids) !== JSON.stringify(baseline.values['notify.system_channel_ids'])) values['notify.system_channel_ids'] = ids;
    try {
      const snapshot = apiData(await api.PATCH('/api/v1/settings', { body: { values }, signal: controller.signal }));
      if (!operation.isCurrent(controller)) return;
      queryClient.setQueryData(apiKey('settings'), snapshot); setBaseline(snapshot); setDraft(readNotificationRules(snapshot)); setSaved(true);
    } catch (failure) { if (operation.isCurrent(controller)) setError(failure); }
    finally { if (operation.isCurrent(controller)) setSaving(false); }
  }
  const allChannels: ChannelRow[] = channels.data ?? [];
  const displayMaximum = /^\d+$/.test(draft.maximum) && Number.isSafeInteger(Number(draft.maximum)) && Number(draft.maximum) > 0 ? Number(draft.maximum) : null;
  return <FormSection title={t('web.notify.rules_title')} description={t('web.notify.rules_help')}>
    <form className="space-y-group" onSubmit={(event) => { event.preventDefault(); void save(); }} onKeyDown={(event) => { if (event.nativeEvent.isComposing && event.key === 'Enter') event.preventDefault(); }}>
      {error !== null && <InlineMessage tone="danger"><p>{errorText(error)}</p>{error instanceof ApiError && error.requestID && <p>{t('web.request_id', { request_id: error.requestID })}</p>}</InlineMessage>}
      {saved && <InlineMessage tone="positive">{t('web.notify.rules_saved')}</InlineMessage>}
      <RadioGroup label={t('settings.notify.link_target.label')} value={draft.link} disabled={saving} options={[{ value: 'mobile', label: t('web.notify.mobile_link') }, { value: 'desktop', label: t('web.notify.desktop_link') }]} hint={hint('notify.link_target')} error={fieldError('notify.link_target')} onValueChange={(value) => { if (value === 'mobile' || value === 'desktop') change({ link: value }); }} />
      <TextField label={t('settings.notify.max_per_run.label')} inputMode="numeric" value={draft.maximum} disabled={saving} hint={hint('notify.max_per_run')} error={invalid === 'notify.max_per_run' ? t('web.notify.validation.positive_integer') : fieldError('notify.max_per_run')} onChange={(event) => change({ maximum: event.target.value })} />
      <p className="text-body text-text-secondary">{displayMaximum === null ? null : displayMaximum === 1 ? t('web.notify.merge_single_limit') : t('web.notify.merge_limit', { limit: draft.maximum, individual: displayMaximum - 1 })}</p>
      <div className="space-y-md">
        <RadioGroup label={t('settings.notify.system_channel_ids.label')} value={draft.all ? 'all' : 'selected'} disabled={saving} options={[{ value: 'all', label: t('web.notify.system_all_enabled') }, { value: 'selected', label: t('web.notify.system_selected') }]} hint={hint('notify.system_channel_ids')} onValueChange={(value) => change({ all: value === 'all' })} />
        {!draft.all && <fieldset disabled={saving || !channels.data} className="space-y-sm"><legend className="sr-only">{t('web.notify.system_selected')}</legend>
          {channels.isPending && <p role="status">{t('common.loading')}</p>}
          {channels.error && <p role="alert">{errorText(channels.error)}</p>}
          {allChannels.map((channel) => <Checkbox key={channel.id} label={channel.name} hint={t(channelCatalog[channel.kind].label) + ' · ' + (channel.enabled ? t('web.notify.enabled') : t('web.notify.disabled'))} checked={draft.ids.includes(channel.id)} onCheckedChange={(checked) => change({ ids: checked ? [...draft.ids, channel.id] : draft.ids.filter((id) => id !== channel.id) })} />)}
          {draft.ids.filter((id) => !allChannels.some((channel) => channel.id === id)).map((id) => channels.data ? <Checkbox key={id} label={t('web.notify.unknown_target', { id })} checked onCheckedChange={(checked) => { if (!checked) change({ ids: draft.ids.filter((value) => value !== id) }); }} /> : <p key={id} className="text-body text-text-secondary">{t('web.notify.unknown_target', { id })}</p>)}
        </fieldset>}
        {(invalid === 'notify.system_channel_ids' || fieldError('notify.system_channel_ids')) && <p role="alert" className="text-body text-danger">{invalid === 'notify.system_channel_ids' ? t('web.notify.choose_system_target') : fieldError('notify.system_channel_ids')}</p>}
        <p className="text-body text-text-secondary">{t('web.notify.system_empty_is_all')}</p>
      </div>
      {baseline.warnings.filter((warning) => warning.key.startsWith('notify.')).map((warning) => <InlineMessage key={warning.key} tone="warning">{messageText(warning.reason)}</InlineMessage>)}
      <Button type="submit" variant="primary" busy={saving} busyLabel={t('web.notify.saving')}>{t('web.notify.save_rules')}</Button>
    </form>
  </FormSection>;
}

export function NotificationsPage({ adapter: supplied }: { adapter?: ChannelAdapter | undefined } = {}) {
  const api = useApi(); const adapter = useChannelAdapter(supplied); const { t } = useTranslation(); const errorText = useApiError();
  const settings = useQuery({ queryKey: apiKey('settings'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings', { signal })) });
  const definitions = useQuery({ queryKey: apiKey('setting-definitions'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings/definitions', { signal })) });
  const timeZone = typeof settings.data?.values['general.timezone'] === 'string' ? settings.data.values['general.timezone'] : 'UTC';
  return <div className="space-y-xxl">
    <PageHeader title={t('web.notify.title')} description={t('web.notify.page_help')} />
    <ChannelSetup adapter={adapter} timeZone={timeZone} />
    <p className="text-body text-text-secondary">{t('web.notify.test_eligibility')} <Link className="inline-flex min-h-control items-center text-accent-text underline underline-offset-4" to="/tasks">{t('web.notify.manage_tasks')}</Link></p>
    {(settings.isPending || definitions.isPending) && <StatePanel kind="pending" label={t('common.loading')}><Skeleton /><Skeleton /></StatePanel>}
    {(settings.error || definitions.error) && <StatePanel kind="error" title={t('web.notify.rules_failed')} description={errorText(settings.error ?? definitions.error)} retry={{ label: t('common.retry'), run: () => { void settings.refetch(); void definitions.refetch(); } }} />}
    {settings.data && definitions.data && <RulesForm initial={settings.data} definitions={definitions.data} adapter={adapter} />}
    <FormSection title={t('web.notify.behavior_title')}><div className="space-y-md text-body text-text-secondary"><p>{t('web.notify.urgent_help')}</p><p>{t('web.notify.urgent_channels')}</p><p>{t('web.notify.merge_help')}</p><p>{t('web.notify.system_events')}</p><p>{t('web.notify.no_repeat_system')}</p>{settings.data?.values['general.public_url'] === '' && <p>{t('web.notify.no_public_url')} <Link className="inline-flex min-h-control items-center text-accent-text underline underline-offset-4" to="/settings/general">{t('web.notify.configure_public_url')}</Link></p>}<p>{t('web.notify.summary_fallback_link')}</p></div></FormSection>
  </div>;
}
