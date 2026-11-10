import type { Schema } from '../../api/types';
import { useMessageText } from '../../api/hooks';
import { Button, Checkbox, StatusBadge } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { formatDateTime } from '../../i18n/format';

export type ProviderRowProps = {
  name: string;
  baseURL: string;
  visionModel: string;
  textModel: string | null;
  enabled: boolean;
  reason: Schema<'LocalizedMessage'> | null;
  errorAt?: number | null;
  timeZone?: string;
  pending?: boolean;
  first: boolean;
  last: boolean;
  onEdit(): void;
  onTest(): void;
  onMove(direction: -1 | 1): void;
  onEnabled(enabled: boolean): void;
  onDelete(): void;
};

export function ProviderRow({ name, baseURL, visionModel, textModel, enabled, reason, errorAt, timeZone, pending = false, first, last, onEdit, onTest, onMove, onEnabled, onDelete }: ProviderRowProps) {
  const { t, locale } = useTranslation();
  const messageText = useMessageText();
  const label = (action: string) => t('web.ai.provider_action', { action, name });
  return <article className="min-w-0 space-y-md border-b border-border py-lg" aria-label={name}>
    <div className="flex flex-wrap items-center justify-between gap-md">
      <div className="flex min-w-0 flex-wrap items-center gap-sm">
        <h3 className="break-words text-section font-semibold">{name}</h3>
        <StatusBadge tone={!enabled ? 'neutral' : reason ? 'warning' : 'positive'}>{t(!enabled ? 'web.ai.disabled' : reason ? 'web.ai.unavailable' : 'web.ai.available')}</StatusBadge>
      </div>
      <Checkbox label={label(t('web.ai.enabled'))} checked={enabled} disabled={pending} onCheckedChange={onEnabled} />
    </div>
    <p className="break-all text-body text-text-secondary">{baseURL}</p>
    <dl className="grid min-w-0 gap-md sm:grid-cols-2">
      <div className="min-w-0"><dt className="text-caption text-text-secondary">{t('web.ai.vision_model')}</dt><dd className="break-all text-body">{visionModel}</dd></div>
      <div className="min-w-0"><dt className="text-caption text-text-secondary">{t('web.ai.text_model')}</dt><dd className="break-all text-body">{textModel || t('web.ai.text_fallback')}</dd></div>
    </dl>
    {reason && <div className="space-y-sm">
      <p className="text-body text-warning">{messageText(reason)}</p>
      {errorAt !== null && errorAt !== undefined && timeZone && <p className="text-caption text-text-secondary">{t('web.ai.error_time', { time: formatDateTime(errorAt, locale, timeZone) })}</p>}
      <p className="text-body text-text-secondary">{t(reason.code === 'analysis.client.balance' || reason.code === 'analysis.client.daily_quota' ? 'web.ai.provider_probe_wait' : 'web.ai.provider_manual_fix')}</p>
    </div>}
    <div className="flex flex-wrap gap-sm">
      <Button aria-label={label(t('web.ai.edit_provider'))} disabled={pending} onClick={onEdit}>{t('web.ai.edit_provider')}</Button>
      <Button aria-label={label(t('web.ai.test'))} disabled={pending} onClick={onTest}>{t('web.ai.test')}</Button>
      <Button aria-label={label(t('web.ai.move_up'))} disabled={pending || first} onClick={() => onMove(-1)}>{t('web.ai.move_up')}</Button>
      <Button aria-label={label(t('web.ai.move_down'))} disabled={pending || last} onClick={() => onMove(1)}>{t('web.ai.move_down')}</Button>
      <Button aria-label={label(t('web.ai.delete'))} variant="plain" disabled={pending} onClick={onDelete}>{t('web.ai.delete')}</Button>
    </div>
  </article>;
}
