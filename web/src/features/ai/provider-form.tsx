import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useAuthentication } from '../../app/auth-state';
import { useUnsavedChanges } from '../../app/unsaved-changes';
import { Button, Checkbox, SecretField, SelectField, TextField } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { emptyProviderDraft, type ProviderDraft } from './draft';
import { providerPresets } from './presets';

export type ProviderFormProps = {
  initial?: ProviderDraft;
  apiKeySet?: boolean;
  pending?: boolean;
  error?: string;
  fieldErrors?: Readonly<Record<string, string>>;
  onSave(draft: ProviderDraft): void;
  onCancel?(): void;
  onDirtyChange?(dirty: boolean): void;
};

export function ProviderForm({ initial, apiKeySet = false, pending = false, error, fieldErrors = {}, onSave, onCancel, onDirtyChange }: ProviderFormProps) {
  const { t } = useTranslation();
  const authentication = useAuthentication();
  const [draft, setDraft] = useState<ProviderDraft>(() => initial ?? emptyProviderDraft());
  const formRef = useRef<HTMLFormElement>(null);
  const fieldErrorSummary = Object.values(fieldErrors).join('\n');
  const baseline = initial ?? emptyProviderDraft();
  const dirty = (Object.keys(draft) as (keyof ProviderDraft)[]).some((key) => draft[key] !== baseline[key]);
  useUnsavedChanges(dirty);
  useEffect(() => { onDirtyChange?.(dirty); }, [dirty, onDirtyChange]);
  useEffect(() => {
    if (authentication?.authenticated === false) setDraft((current) => ({ ...current, keyValue: '', keyAction: 'keep' }));
  }, [authentication?.authenticated]);
  useEffect(() => {
    if (fieldErrorSummary) formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
  }, [fieldErrorSummary]);
  const change = <K extends keyof ProviderDraft>(key: K, value: ProviderDraft[K]) => setDraft((current) => ({ ...current, [key]: value }));
  const presetHint = draft.preset === 'aliyun' ? t('web.ai.preset_aliyun_help')
    : draft.preset === 'anthropic' ? t('web.ai.preset_anthropic_help')
      : draft.preset === 'ollama' ? t('web.ai.preset_ollama_help') : '';
  function selectPreset(value: string) {
    const preset = providerPresets.find((item) => item.value === value);
    if (preset) setDraft((current) => ({ ...current, preset: preset.value, baseURL: preset.baseURL ?? current.baseURL }));
  }
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!pending && authentication?.authenticated !== false) onSave({ ...draft });
  }
  return (
    <form ref={formRef} onSubmit={submit} className="min-w-0 space-y-lg" aria-busy={pending}>
      <fieldset disabled={pending} className="min-w-0 space-y-lg">
        <TextField label={t('web.ai.name')} value={draft.name} onChange={(event) => change('name', event.target.value)} required autoComplete="off" {...(fieldErrors.name ? { error: fieldErrors.name } : {})} />
        <SelectField label={t('web.ai.preset')} value={draft.preset} onValueChange={selectPreset} options={providerPresets.map((preset) => ({ value: preset.value, label: t(preset.labelKey) }))} />
        <TextField label={t('web.ai.base_url')} value={draft.baseURL} onChange={(event) => change('baseURL', event.target.value)} required type="url" inputMode="url" autoComplete="off" autoCapitalize="none" autoCorrect="off" spellCheck={false} hint={[t('web.ai.protocol_hint'), presetHint].filter(Boolean).join(' ')} {...(fieldErrors.base_url ? { error: fieldErrors.base_url } : {})} />
        {draft.preset === 'anthropic' && <a href="https://platform.claude.com/docs/en/cli-sdks-libraries/libraries/openai-sdk" target="_blank" rel="noreferrer" className="inline-flex min-h-control items-center text-body text-accent-text underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus">{t('web.ai.preset_anthropic_docs')}</a>}
        {/\/chat\/completions\/*$/.test(draft.baseURL) && <p className="text-body text-warning">{t('web.ai.url_normalize_pending')}</p>}
        {apiKeySet && <SelectField label={t('web.ai.api_key')} hint={t('web.ai.key_set')} value={draft.keyAction} onValueChange={(value) => {
          if (value === 'keep' || value === 'replace' || value === 'remove') setDraft((current) => ({ ...current, keyAction: value, keyValue: '' }));
        }} options={[
          { value: 'keep', label: t('web.ai.key_keep') }, { value: 'replace', label: t('web.ai.key_replace') }, { value: 'remove', label: t('web.ai.key_remove') },
        ]} />}
        {(!apiKeySet || draft.keyAction === 'replace') && <SecretField label={t('web.ai.api_key')} showLabel={t('web.ai.show_key')} hideLabel={t('web.ai.hide_key')} value={draft.keyValue} onChange={(event) => setDraft((current) => ({ ...current, keyValue: event.target.value, keyAction: 'replace' }))} autoComplete="new-password" autoCapitalize="none" autoCorrect="off" spellCheck={false} {...(fieldErrors.api_key ? { error: fieldErrors.api_key } : {})} />}
        <TextField label={t('web.ai.vision_model')} value={draft.visionModel} onChange={(event) => change('visionModel', event.target.value)} required autoComplete="off" autoCapitalize="none" autoCorrect="off" spellCheck={false} {...(fieldErrors.vision_model ? { error: fieldErrors.vision_model } : {})} />
        <TextField label={t('web.ai.text_model')} hint={t('web.ai.text_fallback')} value={draft.textModel} onChange={(event) => change('textModel', event.target.value)} autoComplete="off" autoCapitalize="none" autoCorrect="off" spellCheck={false} {...(fieldErrors.text_model ? { error: fieldErrors.text_model } : {})} />
        <Checkbox label={t('web.ai.enabled')} checked={draft.enabled} onCheckedChange={(value) => change('enabled', value)} />
      </fieldset>
      {error && <p role="alert" className="text-body text-danger">{error}</p>}
      <div className="flex flex-wrap gap-sm">
        <Button type="submit" variant="primary" busy={pending} busyLabel={t('web.confirm_pending')}>{t('web.ai.save')}</Button>
        {onCancel && <Button onClick={onCancel}>{t('common.cancel')}</Button>}
      </div>
    </form>
  );
}
