import { useEffect, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { apiData, useApi, useApiError, useMessageText } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import { ApiError, isCancellation } from '../../api/errors';
import type { components } from '../../api/schema.gen';
import { useTranslation } from '../../i18n';
import { Button } from '../../components/ui/button';
import { Dialog } from '../../components/ui/dialog';
import { TextField } from '../../components/ui/text-field';
import { FormSection, InlineMessage } from '../../components/ui';
import { useCancellableAction } from './use-cancellable-action';

type CleanupResult = components['schemas']['CleanupResult'];

export function Maintenance({ disabled, onBusyChange }: { disabled: boolean; onBusyChange?(busy: boolean): void }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const { t } = useTranslation();
  const text = useMessageText();
  const errorText = useApiError();
  const [open, setOpen] = useState(false);
  const [confirmation, setConfirmation] = useState('');
  const [pending, setPending] = useState<'images' | 'data' | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [uncertain, setUncertain] = useState(false);
  const [result, setResult] = useState<{ kind: 'images' | 'data'; value: CleanupResult } | null>(null);
  const action = useCancellableAction(() => { setPending(null); setUncertain(true); onBusyChange?.(false); });
  useEffect(() => { if (!action.enabled) { setOpen(false); setConfirmation(''); } }, [action.enabled]);

  async function clear(kind: 'images' | 'data') {
    if (disabled || pending || !action.isActive() || (kind === 'data' && confirmation !== '清除')) return;
    const abort = action.start();
    if (!abort) return;
    setPending(kind); onBusyChange?.(true); setError(null); setResult(null); setUncertain(false);
    try {
      let value: CleanupResult;
      if (kind === 'images') {
        value = apiData(await api.POST('/api/v1/maintenance/clear-image-cache', { signal: abort.signal }));
      } else {
        if (confirmation !== '清除') return;
        value = apiData(await api.POST('/api/v1/maintenance/clear-data', { body: { confirmation }, signal: abort.signal }));
      }
      if (!action.isCurrent(abort)) return;
      setResult({ kind, value }); if (kind === 'data') { setOpen(false); setConfirmation(''); }
      if (kind === 'data') {
        void queryClient.invalidateQueries({ predicate: (query) => query.queryKey[0] === 'api' });
      } else {
        void queryClient.invalidateQueries({ queryKey: apiKey('system-status') });
      }
    } catch (failure) {
      if (action.isCurrent(abort)) {
        if (isCancellation(failure) || abort.signal.aborted) setUncertain(true);
        else { setError(failure); if (!(failure instanceof ApiError) || failure.status === 0 || failure.status >= 500) setUncertain(true); }
      }
    } finally {
      if (action.finish(abort)) { setPending(null); onBusyChange?.(false); }
    }
  }

  const feedback = <>
    {error !== null && <InlineMessage tone="danger"><p>{errorText(error)}</p>{error instanceof ApiError && error.requestID && <p className="mt-sm break-all text-caption">{t('web.request_id', { request_id: error.requestID })}</p>}</InlineMessage>}
    {uncertain && <InlineMessage tone="warning">{t('web.settings.clear_data_uncertain')}</InlineMessage>}
  </>;
  return <FormSection title={t('web.settings.maintenance_section')} description={t('web.settings.maintenance_description')}>
    <div className="flex flex-wrap gap-md">
      <Button busy={pending === 'images'} disabled={disabled || !action.enabled || pending !== null} onClick={() => { void clear('images'); }}>{t('web.settings.clear_images')}</Button>
      <Button variant="destructive" disabled={disabled || !action.enabled || pending !== null} onClick={() => { setOpen(true); setError(null); setUncertain(false); }}>{t('web.settings.clear_data_title')}</Button>
      {pending === 'images' && <Button onClick={action.cancel}>{t('web.settings.stop_waiting')}</Button>}
    </div>
    {!open && feedback}
    {result && <InlineMessage tone={result.value.warnings.length ? 'warning' : 'positive'} title={result.value.warnings.length ? t('web.settings.cleanup_attention') : t(result.kind === 'images' ? 'web.settings.clear_images_done' : 'web.settings.clear_data_done')}>
      {result.kind === 'data' && <p>{t('web.settings.deleted_records', { count: Object.values(result.value.deleted).reduce((total, count) => total + count, 0) })}</p>}
      <p>{t('web.settings.deleted_images', { count: result.value.images_deleted })}</p>
      {result.value.warnings.map((warning, index) => <p key={`${warning.code}-${index}`} className="mt-sm">{text(warning)}</p>)}
    </InlineMessage>}
    <Dialog open={open && action.enabled} onOpenChange={(next) => { if (pending === 'data') return; setOpen(next); }} title={t('web.settings.clear_data_title')} closeLabel={t('common.close')} description={t('web.settings.clear_data_description')}>
      <div className="space-y-xl">
        <p className="text-body">{t('web.settings.clear_data_deleted')}</p>
        <p className="text-body">{t('web.settings.clear_data_preserved')}</p>
        <InlineMessage tone="warning">{t('web.settings.clear_data_quotas_kept')}</InlineMessage>
        <TextField label={t('web.settings.clear_data_type_word')} value={confirmation} autoComplete="off" disabled={pending === 'data'} onChange={(event) => setConfirmation(event.currentTarget.value)} {...(error instanceof ApiError && error.fields.some((field) => field.key === 'confirmation') ? { error: error.fields.filter((field) => field.key === 'confirmation').map((field) => text(field.reason)).join(' ') } : {})} />
        {feedback}
        <div className="flex flex-wrap justify-end gap-sm">
          {pending === 'data'
            ? <Button onClick={action.cancel}>{t('web.settings.stop_waiting')}</Button>
            : <Button onClick={() => setOpen(false)}>{t('common.cancel')}</Button>}
          <Button variant="destructive" busy={pending === 'data'} disabled={confirmation !== '清除' || disabled || !action.enabled || pending !== null} onClick={() => { void clear('data'); }}>{t('web.settings.clear_data_confirm')}</Button>
        </div>
      </div>
    </Dialog>
  </FormSection>;
}
