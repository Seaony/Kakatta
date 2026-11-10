import type { Schema } from '../../api/types';
import { useTranslation } from '../../i18n';

export function TrialRecovery({ reason }: { reason: Schema<'LocalizedMessage'> | null }) {
  const { t } = useTranslation();
  if (!reason) return null;
  const key = reason.code === 'analysis.client.auth' ? 'web.ai.auth_help'
    : reason.code === 'analysis.client.not_found' ? 'web.ai.model_help'
    : reason.code === 'analysis.client.unsupported_image' ? 'web.ai.image_help'
    : reason.code === 'analysis.client.localhost' ? 'web.ai.localhost_help'
    : reason.code === 'analysis.client.balance' || reason.code === 'analysis.client.daily_quota' ? 'web.ai.provider_probe_wait'
    : reason.code === 'analysis.client.network' || reason.code === 'analysis.client.timeout' || reason.code === 'analysis.client.server' || reason.code === 'analysis.client.rate_limit' ? 'web.ai.network_help'
    : 'web.ai.provider_manual_fix';
  return <p className="text-body text-text-secondary">{t(key)}</p>;
}
