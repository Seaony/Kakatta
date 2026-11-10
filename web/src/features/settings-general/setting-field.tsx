import { SelectField, Switch } from '../../components/ui';
import { TextField } from '../../components/ui/text-field';
import { StatusBadge } from '../../components/ui/status-badge';
import { useMessageText } from '../../api/hooks';
import { useTranslation } from '../../i18n';
import type { Definition, Draft, JsonValue, Message, Snapshot } from './model';
import { draftWarnings } from './model';

export function fieldID(key: string) { return `setting-${key.replaceAll('.', '-')}`; }

export function SettingField({ definition, draft, source, errors, warnings, disabled, onChange }: {
  definition: Definition;
  draft: Draft;
  source: Snapshot['sources'][string] | undefined;
  errors: readonly Message[];
  warnings: readonly Message[];
  disabled: boolean;
  onChange(draft: Draft): void;
}) {
  const { t } = useTranslation();
  const text = useMessageText();
  const label = text(definition.label);
  const id = fieldID(definition.key);
  const unit = definition.unit ? text({ code: `units.${definition.unit}`, args: { count: 1 } }) : '';
  const error = errors.length ? errors.map(text).join(' ') : undefined;
  const readOnly = source === 'environment';
  const locked = disabled || readOnly;
  const notices = [...warnings, ...draftWarnings(definition, draft)].filter((message, index, all) => all.findIndex((item) => item.code === message.code && JSON.stringify(item.args) === JSON.stringify(message.args)) === index);

  function optionLabel(value: JsonValue): string {
    if (definition.key === 'general.language') {
      if (value === 'system') return t('web.settings.follow_system');
      if (value === 'zh-CN') return t('web.settings.language_zh');
      if (value === 'en') return t('web.settings.language_en');
    }
    if (definition.key === 'general.appearance') {
      if (value === 'system') return t('web.settings.follow_system');
      if (value === 'light') return t('web.settings.appearance_light');
      if (value === 'dark') return t('web.settings.appearance_dark');
    }
    if (definition.key === 'retention.findings_days' && typeof value === 'number') return value === 0 ? t('web.settings.forever') : t('web.settings.days_value', { count: value });
    if (typeof value === 'boolean') return t(value ? 'web.settings.enabled' : 'web.settings.disabled');
    if (typeof value === 'string' || typeof value === 'number') return String(value);
    return JSON.stringify(value);
  }

  function displayDefault() {
    const value = definition.default;
    if (typeof value === 'object' && value !== null && !Array.isArray(value)) {
      if ('min' in value && 'max' in value) return `${String(value.min)}–${String(value.max)} ${unit}`;
      if ('start' in value && 'end' in value) return `${t(value.enabled ? 'web.settings.enabled' : 'web.settings.disabled')} · ${String(value.start)}–${String(value.end)}`;
    }
    const display = optionLabel(value);
    return display ? `${display}${unit && !definition.options.length ? ` ${unit}` : ''}` : t('web.settings.empty_value');
  }

  return <div id={id} tabIndex={-1} className="min-w-0 scroll-mt-lg space-y-md border-b border-border py-xl last:border-b-0">
    <div className="flex flex-wrap items-center gap-sm">
      {source === 'environment' && <StatusBadge tone="warning">{t('web.settings.environment_readonly')}</StatusBadge>}
      {definition.task_override && <StatusBadge tone="neutral">{t('web.settings.task_override_badge')}</StatusBadge>}
    </div>
    {draft.kind === 'scalar' && (definition.options.length > 0
      ? <SelectField label={label} value={draft.text} disabled={locked} {...(error ? { error } : {})} options={definition.options.filter((value) => typeof value === 'string' || typeof value === 'number').map((value) => ({ value: String(value), label: optionLabel(value) }))} onValueChange={(value) => onChange({ kind: 'scalar', text: value })} />
      : <TextField label={unit ? `${label} (${unit})` : label} value={draft.text} disabled={locked} {...(error ? { error } : {})} inputMode={definition.kind === 'integer' ? 'numeric' : undefined} autoComplete="off" spellCheck={false} onChange={(event) => onChange({ kind: 'scalar', text: event.currentTarget.value })} />)}
    {draft.kind === 'boolean' && <Switch label={label} checked={draft.checked} disabled={locked} {...(error ? { error } : {})} onCheckedChange={(checked) => onChange({ kind: 'boolean', checked })} />}
    {draft.kind === 'range' && <fieldset className="min-w-0 space-y-sm" disabled={locked}>
      <legend className="text-body font-medium">{label}</legend>
      <div className="grid min-w-0 gap-md sm:grid-cols-2">
        <TextField label={t('web.settings.range_min', { label, unit })} value={draft.min} inputMode="decimal" onChange={(event) => onChange({ ...draft, min: event.currentTarget.value })} {...(error ? { error } : {})} />
        <TextField label={t('web.settings.range_max', { label, unit })} value={draft.max} inputMode="decimal" onChange={(event) => onChange({ ...draft, max: event.currentTarget.value })} />
      </div>
    </fieldset>}
    {draft.kind === 'quiet_hours' && <fieldset className="min-w-0 space-y-md" disabled={locked}>
      <legend className="sr-only">{label}</legend>
      <Switch label={label} checked={draft.enabled} disabled={locked} onCheckedChange={(enabled) => onChange({ ...draft, enabled })} />
      <div className="grid min-w-0 gap-md sm:grid-cols-2">
        <TextField label={t('web.settings.quiet_start')} type="time" value={draft.start} disabled={!draft.enabled || locked} onChange={(event) => onChange({ ...draft, start: event.currentTarget.value })} {...(error ? { error } : {})} />
        <TextField label={t('web.settings.quiet_end')} type="time" value={draft.end} disabled={!draft.enabled || locked} onChange={(event) => onChange({ ...draft, end: event.currentTarget.value })} />
      </div>
      <p className="text-body text-text-secondary">{t('web.settings.quiet_cross_midnight')}</p>
    </fieldset>}
    <dl className="grid min-w-0 gap-x-lg gap-y-sm text-body sm:grid-cols-[max-content_minmax(0,1fr)]">
      <dt className="font-medium text-text-label">{t('web.settings.default_label')}</dt><dd className="min-w-0 break-words tabular-nums">{displayDefault()}</dd>
      <dt className="font-medium text-text-label">{t('web.settings.recommendation_label')}</dt><dd className="min-w-0 break-words">{text(definition.recommendation.text)}</dd>
      <dt className="font-medium text-text-label">{t('web.settings.why_label')}</dt><dd className="min-w-0 break-words">{text(definition.why)}</dd>
      <dt className="font-medium text-text-label">{t('web.settings.risk_label')}</dt><dd className="min-w-0 break-words">{text(definition.risk)}</dd>
    </dl>
    {notices.map((message) => <p key={`${message.code}-${JSON.stringify(message.args)}`} role="status" className="rounded-control bg-warning-fill px-md py-sm text-body font-medium text-warning">{text(message)}</p>)}
  </div>;
}
