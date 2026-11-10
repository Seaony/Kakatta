import { useState } from 'react';
import { Link } from '@tanstack/react-router';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { ApiError } from '../../api/errors';
import { apiData, useApi, useApiError, useMessageText } from '../../api/hooks';
import type { Schema } from '../../api/types';
import { Button, Dialog, InlineMessage, RadioGroup, SelectField, StatusBadge } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { invalidateResults, profileFields, type Finding } from './data';
import { attitudeKeys, checkKeys, Evidence } from './presentation';

function FieldIssues({ error }: { error: unknown }) {
  const message = useMessageText();
  if (!(error instanceof ApiError) || error.fields.length === 0) return null;
  return <ul className="mt-sm space-y-sm">{error.fields.map((field, index) => <li key={`${field.key}:${index}`}>{message(field.reason)}</li>)}</ul>;
}

function isStatus(value: string): value is Schema<'AnalysisCheckStatus'> {
  return value === 'ok' || value === 'risk' || value === 'suspect' || value === 'unknown';
}

export function Risks({ finding, dictionary }: { finding: Finding; dictionary: Schema<'AnalysisRiskDefinition'>[] }) {
  const { t } = useTranslation();
  const message = useMessageText();
  const risks = profileFields(finding.task.profile)?.risks ?? [];
  const [editing, setEditing] = useState<Schema<'AnalysisProfileRisk'> | null>(null);
  const [status, setStatus] = useState<Schema<'AnalysisCheckStatus'>>('unknown');
  const api = useApi();
  const errorText = useApiError();
  const client = useQueryClient();
  const mutation = useMutation({
    mutationFn: async (risk: Schema<'AnalysisProfileRisk'>) => apiData(await api.PUT('/api/v1/findings/{finding_id}/overrides', {
      params: { path: { finding_id: finding.id } },
      body: { risks: { ...finding.user_overrides.risks, [risk.id]: { status, rev: risk.rev } } },
    })),
    onSuccess: async () => { setEditing(null); await invalidateResults(client); },
  });
  function name(risk: Schema<'AnalysisProfileRisk'>) {
    return dictionary.some((entry) => entry.id === risk.id)
      ? message({ code: `analysis.risk_name.${risk.id}`, args: {} })
      : risk.name ?? risk.id;
  }
  return <div className="space-y-lg">
    {risks.length === 0 && <p className="text-body text-text-secondary">{t('web.detail.check_empty')}</p>}
    <ul className="divide-y divide-border">{risks.map((risk) => {
      const fact = finding.analysis?.checks.find((check) => check.key === risk.id && check.rev === risk.rev);
      const current = finding.checks.find((check) => check.key === risk.id);
      return <li key={risk.id} id={`finding-${finding.id}-risk-${encodeURIComponent(risk.id)}`} tabIndex={-1} className="scroll-mt-xl min-w-0 space-y-md py-lg first:pt-0">
        <div className="flex flex-wrap items-start justify-between gap-md">
          <div className="min-w-0 space-y-sm"><h3 className="break-words text-body font-semibold">{name(risk)}</h3><p className="text-caption text-text-secondary">{t(attitudeKeys[risk.attitude])}</p></div>
          <Button onClick={() => { mutation.reset(); setEditing(risk); setStatus(current?.status ?? 'unknown'); }}>{t('web.detail.edit_check', { name: name(risk) })}</Button>
        </div>
        <div className="flex flex-wrap items-center gap-sm"><span className="text-body">{t('web.detail.current_conclusion')}</span><StatusBadge tone={current?.source === 'image' || current?.status === 'suspect' ? 'warning' : current?.status === 'risk' ? 'danger' : 'neutral'}>{t(checkKeys[current?.status ?? 'unknown'])}</StatusBadge>{current?.source === 'user' && <span className="text-caption">{t('web.detail.manual_conclusion')}</span>}</div>
        <Evidence source={current?.source ?? null} evidence={current?.evidence ?? null} />
        {fact && <details><summary className="min-h-control cursor-pointer py-md text-body text-text-secondary">{t('web.detail.model_conclusion')} · {t(checkKeys[fact.status])}</summary><Evidence source={fact.source} evidence={fact.evidence} /></details>}
      </li>;
    })}</ul>
    <Dialog open={editing !== null} onOpenChange={(open) => { if (!open && !mutation.isPending) setEditing(null); }} title={t('web.detail.edit_check', { name: editing ? name(editing) : '' })} description={t('web.detail.check_scope')} closeLabel={t('common.close')}>
      {editing && <form className="space-y-lg" onSubmit={(event) => { event.preventDefault(); mutation.mutate(editing); }}>
        <RadioGroup label={t('web.detail.current_conclusion')} value={status} onValueChange={(value) => { if (isStatus(value)) setStatus(value); }} disabled={mutation.isPending} options={(['ok', 'risk', 'suspect', 'unknown'] as const).map((value) => ({ value, label: t(checkKeys[value]) }))} />
        {mutation.error && <InlineMessage tone="danger">{errorText(mutation.error)}<FieldIssues error={mutation.error} />{mutation.error instanceof ApiError && mutation.error.requestID && <p className="mt-sm break-all text-caption">{mutation.error.requestID}</p>}</InlineMessage>}
        <Button type="submit" variant="primary" busy={mutation.isPending}>{t('web.detail.save')}</Button>
      </form>}
    </Dialog>
  </div>;
}

export function ExtraFindings({ finding }: { finding: Finding }) {
  const { t } = useTranslation();
  const api = useApi();
  const client = useQueryClient();
  const errorText = useApiError();
  const [affected, setAffected] = useState<number | null>(null);
  const mutation = useMutation({
    mutationFn: async (extra: Schema<'AnalysisExtraFinding'>) => apiData(await api.POST('/api/v1/findings/{finding_id}/extra-check', {
      params: { path: { finding_id: finding.id } }, body: { extra_finding: extra },
    })),
    onSuccess: async (result) => { setAffected(result.effects.needs_reanalysis_count); await invalidateResults(client); },
  });
  const extras = finding.analysis?.extra_findings ?? [];
  return <div className="space-y-lg">
    <h3 className="text-body font-semibold">{t('web.detail.extra_title')}</h3>
    {extras.length === 0 && <p className="text-body text-text-secondary">{t('web.detail.extra_empty')}</p>}
    {extras.map((extra, index) => <div key={`${extra.name}-${index}`} className="space-y-md border-b border-border pb-lg">
      <h4 className="break-words text-body font-medium">{extra.name}</h4><Evidence source={extra.source} evidence={extra.evidence} />
      <Button busy={mutation.isPending && mutation.variables === extra} disabled={mutation.isPending} onClick={() => { setAffected(null); mutation.mutate(extra); }}>{t('web.detail.add_check')}</Button>
    </div>)}
    {mutation.error && <InlineMessage tone="danger">{errorText(mutation.error)}<FieldIssues error={mutation.error} /></InlineMessage>}
    {affected !== null && <InlineMessage tone="positive"><p>{t('web.detail.check_added')}</p>{affected > 0 && <><p className="mt-sm">{t('web.detail.other_reanalysis', { count: affected })}</p><Link to="/results" search={{ task_id: finding.task_id, view: 'all' }} className="inline-flex min-h-control items-center underline underline-offset-4">{t('web.detail.back_to_results')}</Link></>}</InlineMessage>}
  </div>;
}

export function SpecsEditor({ finding, open, onOpenChange }: { finding: Finding; open: boolean; onOpenChange(open: boolean): void }) {
  const { t } = useTranslation();
  const api = useApi();
  const client = useQueryClient();
  const errorText = useApiError();
  const message = useMessageText();
  const profile = profileFields(finding.task.profile);
  const [specs, setSpecs] = useState<Schema<'AnalysisSpecValue'>[]>(() => profile?.specs.map((definition) => ({ name: definition.name, value: finding.item_specs?.specs.find((spec) => spec.name === definition.name)?.value ?? null })) ?? []);
  const [brandNew, setBrandNew] = useState<boolean | null>(finding.item_specs?.brand_new ?? null);
  const mutation = useMutation({
    mutationFn: async () => apiData(await api.PUT('/api/v1/tasks/{task_id}/items/{item_id}/specs', {
      params: { path: { task_id: finding.task_id, item_id: finding.item_id } }, body: { specs, brand_new: brandNew },
    })),
    onSuccess: async () => { onOpenChange(false); await invalidateResults(client); },
  });
  return <Dialog open={open} onOpenChange={(next) => { if (!mutation.isPending) onOpenChange(next); }} title={t('web.detail.edit_specs')} description={t('web.detail.spec_scope')} closeLabel={t('common.close')}>
    <form className="space-y-lg" onSubmit={(event) => { event.preventDefault(); mutation.mutate(); }}>
      {(profile?.specs ?? []).map((definition, index) => {
        const current = specs.find((spec) => spec.name === definition.name)?.value ?? null;
        const options = [...definition.options];
        if (current && !options.includes(current)) options.push(current);
        const issue = mutation.error instanceof ApiError ? mutation.error.fields.find((field) => field.key === `specs.${index}.value` || field.key === `specs[${index}].value`) : undefined;
        return <SelectField key={definition.name} label={definition.name} value={current ?? ''} options={[{ value: '', label: t('web.detail.unknown') }, ...options.map((value) => ({ value, label: value }))]} disabled={mutation.isPending} {...(issue ? { error: message(issue.reason) } : {})} onValueChange={(value) => setSpecs((previous) => {
          const next = previous.filter((spec) => spec.name !== definition.name);
          return [...next, { name: definition.name, value: value === '' ? null : value }];
        })} />;
      })}
      <SelectField label={`${t('web.detail.brand_new')} / ${t('web.detail.used')}`} value={brandNew === null ? '' : brandNew ? 'new' : 'used'} options={[{ value: '', label: t('web.detail.unknown') }, { value: 'new', label: t('web.detail.brand_new') }, { value: 'used', label: t('web.detail.used') }]} disabled={mutation.isPending} onValueChange={(value) => setBrandNew(value === '' ? null : value === 'new')} />
      {mutation.error && <InlineMessage tone="danger">{errorText(mutation.error)}<FieldIssues error={mutation.error} /></InlineMessage>}
      <Button type="submit" variant="primary" busy={mutation.isPending}>{t('web.detail.save')}</Button>
    </form>
  </Dialog>;
}
