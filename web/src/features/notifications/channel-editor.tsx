import { useEffect, useId, useRef, useState } from 'react';
import { Button, Checkbox, ConfirmDialog, FormSection, InlineMessage, RadioGroup, SecretField, SelectField, TextAreaField, TextField } from '../../components/ui';
import { useApiError } from '../../api/hooks';
import { useUnsavedChanges } from '../../app/unsaved-changes';
import { useNotificationOperation } from './use-notification-operation';
import { ApiError, isCancellation } from '../../api/errors';
import { useTranslation } from '../../i18n';
import { channelCatalog, channelKinds, webhookVariables, type ChannelField } from './catalog';
import { newChannelDraft, validateChannelDraft, type DraftProblem } from './draft';
import type { ChannelAdapter, ChannelRow, SecretEdit } from './types';

const problemKeys = {
  required: 'web.notify.validation.required', json: 'web.notify.validation.json', object: 'web.notify.validation.object',
  port: 'web.notify.validation.port', url: 'web.notify.validation.url', send_key: 'web.notify.validation.send_key',
} as const;
const selectLabels = { ssl: 'web.notify.smtp.ssl', starttls: 'web.notify.smtp.starttls', none: 'web.notify.smtp.none', GET: 'web.notify.method.get', POST: 'web.notify.method.post', JSON: 'web.notify.content.json', FORM: 'web.notify.content.form' } as const;

function SecretEditor({ field, edit, stored, error, disabled, onChange }: { field: ChannelField; edit: SecretEdit; stored: boolean; error: string | undefined; disabled: boolean; onChange(edit: SecretEdit): void }) {
  const { t } = useTranslation();
  const id = useId();
  const choices = [{ value: 'keep', label: t('web.notify.secret_keep') }, { value: 'replace', label: t('web.notify.secret_replace') }, ...(!field.required ? [{ value: 'clear', label: t('web.notify.secret_clear') }] : [])];
  return <div className="space-y-sm">
    {stored && <RadioGroup label={t(field.label)} value={edit.mode} disabled={disabled} options={choices} onValueChange={(mode) => { if (mode === 'keep' || mode === 'replace' || mode === 'clear') onChange({ mode, value: '' }); }} />}
    {edit.mode === 'keep' && <p className="text-body text-text-secondary">{t('web.notify.secret_set')}</p>}
    {edit.mode === 'clear' && <p className="text-body text-text-secondary">{t('web.notify.secret_will_remove')}</p>}
    {edit.mode === 'replace' && (field.json
      ? <TextAreaField id={id} label={t(field.label)} hint={t('web.notify.secret_replace_full')} value={edit.value} error={error} disabled={disabled} autoComplete="off" spellCheck={false} onChange={(event) => onChange({ mode: 'replace', value: event.target.value })} />
      : <SecretField id={id} label={t(field.label)} hint={t('web.notify.secret_replace_full')} value={edit.value} error={error} disabled={disabled} autoComplete="off" spellCheck={false} showLabel={t('web.notify.secret_show')} hideLabel={t('web.notify.secret_hide')} onChange={(event) => onChange({ mode: 'replace', value: event.target.value })} />)}
    {edit.mode !== 'replace' && error && <p role="alert" className="text-body text-danger">{error}</p>}
  </div>;
}

export function ChannelEditor({ channel, adapter, onSaved, onCancel }: { channel: ChannelRow | null; adapter: ChannelAdapter; onSaved(channel: ChannelRow): void; onCancel(): void }) {
  const { t } = useTranslation();
  const errorText = useApiError();
  const operation = useNotificationOperation();
  const [draft, setDraft] = useState(() => newChannelDraft(channel?.kind ?? 'bark', channel ?? undefined));
  const initial = useRef(JSON.stringify(draft));
  const dirty = JSON.stringify(draft) !== initial.current;
  useUnsavedChanges(dirty);
  const [problems, setProblems] = useState<Record<string, DraftProblem>>({});
  const [serverFields, setServerFields] = useState<Record<string, string>>({});
  const [error, setError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  const [confirmClose, setConfirmClose] = useState(false);
  const form = useRef<HTMLFormElement>(null);
  useEffect(() => {
    if (operation.authenticated === false) {
      setSaving(false); setError(null); setServerFields({});
      setDraft((old) => ({ ...old, secrets: Object.fromEntries(Object.entries(old.secrets).map(([key, edit]) => [key, { ...edit, value: '' }])) }));
    }
  }, [operation.authenticated]);
  const fieldError = (key: string) => serverFields[key] ?? (problems[key] ? t(problemKeys[problems[key]]) : undefined);
  const update = (key: string, value: string) => setDraft((old) => ({ ...old, fields: { ...old.fields, [key]: value } }));
  function close() { if (dirty) setConfirmClose(true); else onCancel(); }
  async function save() {
    if (saving || operation.authenticated === false) return;
    const found = validateChannelDraft(draft);
    setProblems(found); setServerFields({}); setError(null);
    if (Object.keys(found).length) { queueMicrotask(() => form.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus()); return; }
    const controller = operation.begin(); if (!controller) return; setSaving(true);
    try { const saved = await adapter.save(channel?.id ?? null, draft, controller.signal); if (operation.isCurrent(controller)) onSaved(saved); }
    catch (failure) {
      if (operation.isCurrent(controller) && !isCancellation(failure)) {
        setError(failure);
        if (failure instanceof ApiError) setServerFields(Object.fromEntries(failure.fields.map((field) => [field.key.split('.').at(-1) ?? field.key, errorText(new ApiError(failure.status, field.reason))])));
      }
    } finally { if (operation.isCurrent(controller)) setSaving(false); }
  }
  return <FormSection title={channel ? t('web.notify.edit_channel', { name: channel.name }) : t('web.notify.add_channel')}>
    <form ref={form} className="space-y-group" onSubmit={(event) => { event.preventDefault(); void save(); }} onKeyDown={(event) => { if (event.nativeEvent.isComposing && event.key === 'Enter') event.preventDefault(); }}>
      {error !== null && <InlineMessage tone="danger"><p>{errorText(error)}</p>{error instanceof ApiError && error.requestID && <p className="break-all">{t('web.request_id', { request_id: error.requestID })}</p>}</InlineMessage>}
      <SelectField label={t('web.notify.channel_type')} value={draft.kind} disabled={saving} options={channelKinds.map((kind) => ({ value: kind, label: t(channelCatalog[kind].label) }))} onValueChange={(value) => { if (channelKinds.some((kind) => kind === value)) { const kind = channelKinds.find((kind) => kind === value); if (kind) { setDraft({ ...newChannelDraft(kind), name: draft.name, enabled: draft.enabled }); setProblems({}); setServerFields({}); } } }} />
      <TextField label={t('web.notify.channel_name')} value={draft.name} error={fieldError('name')} disabled={saving} onChange={(event) => setDraft((old) => ({ ...old, name: event.target.value }))} />
      <Checkbox label={t('web.notify.enabled')} checked={draft.enabled} disabled={saving} onCheckedChange={(enabled) => setDraft((old) => ({ ...old, enabled }))} />
      <div className="space-y-group">
        {channelCatalog[draft.kind].fields.map((field) => {
          const error = fieldError(field.key);
          const disabled = saving || draft.kind === 'webhook' && draft.fields.method === 'GET' && ['body_template', 'content_type'].includes(field.key);
          if (field.secret) return <SecretEditor key={field.key} field={field} edit={draft.secrets[field.key] ?? { mode: 'replace', value: '' }} stored={channel?.kind === draft.kind && !!channel.hasSecret[field.key]} error={error} disabled={disabled} onChange={(edit) => setDraft((old) => ({ ...old, secrets: { ...old.secrets, [field.key]: edit } }))} />;
          if (field.options) return <SelectField key={field.key} label={t(field.label)} value={draft.fields[field.key] ?? ''} error={error} disabled={disabled} placeholder={t('web.notify.choose_option')} options={field.options.map((value) => ({
            value,
            label: Object.hasOwn(selectLabels, value) ? t(selectLabels[value as keyof typeof selectLabels]) : value,
          }))} onValueChange={(value) => update(field.key, value)} />;
          if (field.json) return <TextAreaField key={field.key} label={t(field.label)} value={draft.fields[field.key] ?? ''} error={error} disabled={disabled} spellCheck={false} className="font-mono" hint={field.key === 'body_template' ? t('web.notify.webhook_default_json') : undefined} onChange={(event) => update(field.key, event.target.value)} />;
          return <TextField key={field.key} label={t(field.label)} value={draft.fields[field.key] ?? ''} error={error} disabled={disabled} inputMode={field.numeric ? 'numeric' : undefined} onChange={(event) => update(field.key, event.target.value)} />;
        })}
      </div>
      <p className="max-w-prose text-body text-text-secondary">{t(channelCatalog[draft.kind].help)}</p>
      {draft.kind === 'webhook' && <details className="text-body"><summary className="min-h-control cursor-pointer py-sm text-text-label">{t('web.notify.webhook_variables')}</summary><p className="py-sm text-text-secondary">{t('web.notify.webhook_variables_help')}</p><ul className="grid gap-sm sm:grid-cols-2">{webhookVariables.map((name) => <li key={name} className="break-words font-mono">{'{{' + name + '}}'}</li>)}</ul>{draft.fields.method === 'GET' && <p>{t('web.notify.webhook_get_no_body')}</p>}</details>}
      <div className="flex flex-wrap gap-sm"><Button type="submit" variant="primary" busy={saving} busyLabel={t('web.notify.saving')}>{t('web.notify.save_channel')}</Button><Button onClick={close} disabled={saving}>{t('common.cancel')}</Button></div>
    </form>
    <ConfirmDialog open={confirmClose} onOpenChange={setConfirmClose} title={t('web.notify.unsaved_close')} confirmLabel={t('web.notify.discard')} cancelLabel={t('web.notify.keep_editing')} onConfirm={onCancel} />
  </FormSection>;
}
