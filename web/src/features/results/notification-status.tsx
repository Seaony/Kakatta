import { StatusBadge } from '../../components/ui/status-badge';
import { useTranslation } from '../../i18n';
import { StoredMessage } from '../../i18n/stored-message';
import { latestDeliveries, resultTime, type Finding } from './model';

export function NotificationStatus({ finding, timeZone }: { finding: Finding; timeZone?: string | undefined }) {
  const { t, locale } = useTranslation();
  const deliveries = latestDeliveries(finding.deliveries);
  const sentAt = finding.notified_at ?? finding.drop_notified_at;
  const sent = sentAt !== null || deliveries.some((delivery) => delivery.ok === true);
  const skipReason = finding.notify_skip_reason;
  const status = finding.notification_status;
  const noNotification = (reason: string) => t('results.notify.none', { reason });
  const title = sent
    ? t('results.notify.sent')
    : status?.state === 'retrying'
      ? t('results.notify.retry_next')
      : status?.state === 'abandoned'
        ? noNotification(t('notify.retry_exhausted'))
        : status?.state === 'cancelled' || skipReason
          ? <StoredMessage message={status?.reason ?? skipReason ?? { code: 'results.notify.cancelled', args: {} }} renderText={noNotification} />
          : finding.notify_kind
            ? t('results.notify.waiting')
            : t('results.notify.not_decided');
  return (
    <details className="min-w-0 text-body">
      <summary className="min-h-control cursor-pointer py-md text-text-label focus-visible:outline focus-visible:outline-2 focus-visible:outline-focus">
        {t('results.notify.title')}: {title}
      </summary>
      <div className="space-y-sm pb-md text-text-secondary">
        {deliveries.length === 0 && sentAt !== null && (
          <p>{t('results.notify.archived')} · {resultTime(sentAt, locale, timeZone)}</p>
        )}
        {deliveries.map((delivery) => (
          <div key={delivery.id} className="flex min-w-0 flex-wrap items-center gap-sm">
            <span className="break-words">{delivery.channel_name ?? t('results.notify.channel_unknown')}</span>
            {delivery.kind === 'recommended' && <span>{t('web.notify.delivery.recommended')}</span>}
            {(delivery.kind === 'all_new' || delivery.kind === 'new') && <span>{t('web.notify.delivery.all_new')}</span>}
            {delivery.kind === 'price_drop' && <span>{t('web.notify.delivery.price_drop')}</span>}
            {delivery.kind === 'summary' && <span>{t('web.notify.delivery.summary')}</span>}
            <StatusBadge tone={delivery.ok === true ? 'positive' : delivery.ok === false ? 'danger' : 'neutral'}>
              {delivery.ok === true ? t('results.notify.sent') : delivery.ok === false ? t('results.notify.failed') : t('results.notify.waiting')}
            </StatusBadge>
            {delivery.at !== null && <time dateTime={new Date(delivery.at).toISOString()}>{resultTime(delivery.at, locale, timeZone)}</time>}
            {delivery.error && <span className="break-words text-danger"><StoredMessage message={delivery.error} /></span>}
            {delivery.kind === 'summary' && delivery.item_count !== null && (
              <span>{t('results.notify.merged', { count: delivery.item_count })}</span>
            )}
          </div>
        ))}
        {status?.reason && (status.state === 'retrying' || status.state === 'abandoned' && status.reason.code !== 'notify.retry_exhausted') && (
          <p className="break-words text-danger"><StoredMessage message={status.reason} /></p>
        )}
        {skipReason && deliveries.length > 0 && <p><StoredMessage message={skipReason} renderText={(reason) => sent ? reason : noNotification(reason)} /></p>}
      </div>
    </details>
  );
}
