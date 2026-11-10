import { useQuery } from '@tanstack/react-query';
import { apiData, useApi, useApiError, useMessageText } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import type { Schema } from '../../api/types';
import { useTranslation } from '../../i18n';
import { Button, Checkbox, InlineMessage, SelectField, TextField } from '../../components/ui';
import { taskOverrideKeys, type TaskDraft } from './model';

export function RegionFields({ draft, onChange, disabled, errorFor }: { draft: TaskDraft; onChange(value: TaskDraft): void; disabled: boolean; errorFor(key: string): string | undefined }) {
  const api = useApi(), apiError = useApiError();
  const { t } = useTranslation();
  const provinces = useQuery({ queryKey: apiKey('tasks', 'regions'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/regions', { signal })), staleTime: Infinity });
  const province = provinces.data?.find((node) => node.name === draft.region_province);
  const cities = useQuery({ queryKey: apiKey('tasks', 'regions', province?.id), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/regions', { params: { query: { parent: province?.id ?? '' } }, signal })), enabled: Boolean(province), staleTime: Infinity });
  const city = cities.data?.find((node) => node.name === draft.region_city);
  const districts = useQuery({ queryKey: apiKey('tasks', 'regions', city?.id), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/regions', { params: { query: { parent: city?.id ?? '' } }, signal })), enabled: Boolean(city), staleTime: Infinity });
  const options = (nodes: Schema<'RegionNode'>[] | undefined, selected: string | null) => {
    const entries = [{ value: '', label: t('tasks.region.any') }, ...(nodes ?? []).map((node) => ({ value: node.name, label: node.name }))];
    if (selected && !nodes?.some((node) => node.name === selected)) entries.push({ value: selected, label: selected });
    return entries;
  };
  const failure = provinces.error ?? cities.error ?? districts.error;
  return <div className="min-w-0 space-y-md">
    <div className="grid min-w-0 gap-lg md:grid-cols-3">
      <SelectField label={t('tasks.field.province')} value={draft.region_province ?? ''} options={options(provinces.data, draft.region_province)} disabled={disabled || provinces.isPending} {...(errorFor('region_province') ? { error: errorFor('region_province') } : {})} onValueChange={(value) => onChange({ ...draft, region_province: value || null, region_city: null, region_district: null })} />
      <SelectField label={t('tasks.field.city')} value={draft.region_city ?? ''} options={options(cities.data, draft.region_city)} disabled={disabled || !province || cities.isPending} {...(errorFor('region_city') ? { error: errorFor('region_city') } : {})} onValueChange={(value) => onChange({ ...draft, region_city: value || null, region_district: null })} />
      <SelectField label={t('tasks.field.district')} value={draft.region_district ?? ''} options={options(districts.data, draft.region_district)} disabled={disabled || !city || districts.isPending || districts.data?.length === 0} {...(errorFor('region_district') ? { error: errorFor('region_district') } : {})} onValueChange={(value) => onChange({ ...draft, region_district: value || null })} />
    </div>
    {failure && <InlineMessage tone="danger" action={<Button onClick={() => { void provinces.refetch(); if (province) void cities.refetch(); if (city) void districts.refetch(); }}>{t('common.retry')}</Button>}>{apiError(failure)}</InlineMessage>}
  </div>;
}

function inputText(value: Schema<'JsonValue'> | undefined): string {
  return typeof value === 'string' || typeof value === 'number' ? String(value) : '';
}

export function OverrideFields({ value, onChange, definitions, globals, disabled, errorFor }: { value: Schema<'SettingValues'>; onChange(value: Schema<'SettingValues'>): void; definitions: Schema<'SettingDefinition'>[]; globals: Schema<'SettingValues'>; disabled: boolean; errorFor(key: string): string | undefined }) {
  const { t } = useTranslation();
  const messageText = useMessageText();
  return <div className="min-w-0 divide-y divide-border">{definitions.filter((definition) => definition.task_override && taskOverrideKeys.some((key) => definition.key === `crawl.${key}`)).map((definition) => {
    const key = definition.key.slice('crawl.'.length);
    const overridden = Object.hasOwn(value, key);
    const effective = overridden ? value[key] : globals[definition.key] ?? definition.default;
    const range = effective !== null && typeof effective === 'object' && !Array.isArray(effective) ? effective : {};
    const label = messageText(definition.label);
    const unit = definition.unit ? messageText({ code: `units.${definition.unit}`, args: { count: typeof definition.default === 'number' ? definition.default : 2 } }) : '';
    const defaultRange = definition.default !== null && typeof definition.default === 'object' && !Array.isArray(definition.default) ? definition.default : {};
    const defaultText = definition.kind === 'range' ? `${inputText(defaultRange.min)}–${inputText(defaultRange.max)}` : inputText(definition.default);
    const minimum = Number(definition.kind === 'range' ? range.min : effective);
    const maximum = Number(definition.kind === 'range' ? range.max : effective);
    const outside = Number.isFinite(minimum) && Number.isFinite(maximum) && (definition.recommendation.min !== null && minimum < definition.recommendation.min || definition.recommendation.max !== null && maximum > definition.recommendation.max);
    const error = errorFor(`crawl_overrides.${key}`);
    return <section key={key} className="min-w-0 space-y-md py-lg">
      <div className="flex min-w-0 flex-wrap items-start justify-between gap-md"><div className="min-w-0 flex-1"><h3 className="text-body font-semibold">{label}</h3><p className="max-w-prose text-body text-text-secondary">{messageText(definition.why)}</p></div><Checkbox label={t('tasks.overrides.follow')} checked={!overridden} disabled={disabled} onCheckedChange={(follow) => {
        const next = { ...value };
        if (follow) delete next[key];
        else next[key] = structuredClone(globals[definition.key] ?? definition.default);
        onChange(next);
      }} /></div>
      {definition.kind === 'range' ? <div className="grid min-w-0 gap-lg sm:grid-cols-2"><TextField label={t('web.settings.range_min', { label, unit })} inputMode="decimal" value={inputText(range.min)} disabled={disabled || !overridden} onChange={(event) => onChange({ ...value, [key]: { ...range, min: event.target.value } })} /><TextField label={t('web.settings.range_max', { label, unit })} inputMode="decimal" value={inputText(range.max)} disabled={disabled || !overridden} onChange={(event) => onChange({ ...value, [key]: { ...range, max: event.target.value } })} /></div> : <TextField label={unit ? `${label} (${unit})` : label} inputMode="numeric" value={inputText(effective)} disabled={disabled || !overridden} onChange={(event) => onChange({ ...value, [key]: event.target.value })} />}
      <dl className="flex min-w-0 flex-wrap gap-x-md gap-y-sm text-body"><dt className="text-text-label">{t('web.settings.default_label')}</dt><dd className="tabular-nums text-text-body">{defaultText}{unit ? ` ${unit}` : ''}</dd></dl>
      <p className="text-body text-text-secondary">{messageText(definition.recommendation.text)}</p><p className="text-body text-text-secondary">{messageText(definition.risk)}</p>{error && <p role="alert" className="text-body text-danger">{error}</p>}
      {outside && <InlineMessage tone="warning">{messageText({ code: 'settings.outside_recommendation', args: {} })}</InlineMessage>}
    </section>;
  })}</div>;
}
