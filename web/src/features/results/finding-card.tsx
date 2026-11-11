import { useState } from 'react';
import { useMessageText } from '../../api/hooks';
import { ActionMenu, Button, Checkbox, CopyButton, StatusBadge } from '../../components/ui';
import { useTranslation, type TranslationKey } from '../../i18n';
import { formatMoney, formatSellerRegistration } from '../../i18n/format';
import { StoredMessage } from '../../i18n/stored-message';
import { effectiveRisks, isOverBudget, listingThumbnailURL, resultTime, safeExternalURL, type Finding, type Listing } from './model';
import { NotificationStatus } from './notification-status';

export type FindingAction = 'favorite' | 'dismiss' | 'restore' | 'block' | 'delete' | 'reanalyze' | 'unread';

const gradeKeys = {
  steal: 'analysis.grade.steal', good: 'analysis.grade.good', fair: 'analysis.grade.fair',
  pricey: 'analysis.grade.pricey', avoid: 'analysis.grade.avoid',
} satisfies Record<NonNullable<Finding['grade']>, TranslationKey>;

export function ItemImage({ listing, findingId }: { listing: Listing; findingId?: number }) {
  const { t } = useTranslation();
  const [failedImage, setFailedImage] = useState<string | null>(null);
  const imageURL = listingThumbnailURL(listing, findingId);
  const imageKey = `${imageURL}|${listing.thumbnail_url}`;
  return imageURL && imageKey !== failedImage
    ? <img key={imageKey} src={imageURL} alt="" width={120} height={120} loading="lazy" referrerPolicy="no-referrer"
      onError={() => setFailedImage(imageKey)} className="aspect-square w-full rounded-thumb object-cover" />
    : <div className="flex aspect-square items-center justify-center rounded-thumb bg-inset p-sm text-center text-caption text-text-secondary">{t('results.card.no_image')}</div>;
}

export function FindingCard({ finding, selected, selectionFull, onSelect, onOpen, onAction, busy, timeZone }: {
  finding: Finding;
  selected: boolean;
  selectionFull: boolean;
  onSelect(selected: boolean): void;
  onOpen(): void;
  onAction(action: FindingAction): void;
  busy: boolean;
  timeZone?: string | undefined;
}) {
  const { t, locale, i18n } = useTranslation();
  const message = useMessageText();
  const listing = finding.listing;
  const link = safeExternalURL(listing.desktop_url);
  const title = listing.title || t('results.card.untitled');
  const computed = finding.decision === 'scored';
  const grade = computed ? finding.grade : null;
  const score = computed ? finding.score : null;
  const heading = grade ? `${score === null ? '' : `${score} `}${t(gradeKeys[grade])}`
    : computed ? t('results.card.unpriced') : finding.decision === 'matched' ? t('results.card.keyword')
      : finding.decision === 'failed' ? t(finding.retryable ? 'results.filter.failed' : 'results.card.failed_final')
        : finding.decision === 'pending' ? t('results.filter.pending')
          : finding.decision === 'skipped' ? t('results.filter.skipped') : t('results.filter.rejected');
  const percent = computed && finding.fair_price !== null && finding.fair_price > 0 && listing.price !== null
    ? Math.round((finding.fair_price - listing.price) / finding.fair_price * 100) : null;
  const risks = effectiveRisks(finding);
  const sellerKind = finding.seller_type;
  const registrationDays = listing.seller_registration_days;
  const registrationTitle = registrationDays !== null && registrationDays > 0 ? t('results.card.registration', { count: registrationDays }) : undefined;
  let sellerReviewSummary: string | undefined;
  if (finding.seller) {
    const recentReviews = finding.seller.recent_rates?.slice(0, 10);
    const sellerReviews = recentReviews?.filter((review) => review.role === 'seller') ?? [];
    if (!recentReviews || recentReviews.some((review) => review.role !== 'seller' && review.role !== 'buyer') ||
      sellerReviews.some((review) => review.kind !== 'good' && review.kind !== 'neutral' && review.kind !== 'bad')) {
      sellerReviewSummary = t('results.card.reviews_unknown');
    } else if (sellerReviews.length === 0) {
      sellerReviewSummary = t('results.card.no_seller_reviews');
    } else {
      const bad = sellerReviews.filter((review) => review.kind === 'bad').length;
      sellerReviewSummary = t(bad > 0 ? 'results.card.bad_reviews' : 'results.card.no_bad_reviews', { count: recentReviews.length, bad });
    }
  }
  return (
    <article className="min-w-0 border-b border-border py-xl" aria-labelledby={`result-${finding.id}-title`}>
      <div className="grid min-w-0 grid-cols-[4.5rem_minmax(0,1fr)] gap-md sm:grid-cols-[7.5rem_minmax(0,1fr)] sm:gap-lg">
        <div><ItemImage listing={listing} findingId={finding.id} /></div>
        <div className="min-w-0 space-y-md">
          <div className="flex min-w-0 flex-wrap items-start justify-between gap-sm">
            <Button variant="plain" onClick={onOpen} className="min-w-0 justify-start px-0 text-left text-section font-semibold" id={`result-${finding.id}-title`}>
              <span className="break-words">{title}</span>
            </Button>
            <div className="min-w-control shrink-0"><Checkbox label="" aria-label={t('results.select.item', { title })} checked={selected} disabled={busy || selectionFull && !selected}
              onCheckedChange={onSelect} /></div>
          </div>
          <div className="flex flex-wrap items-center gap-sm">
            <span className="text-section font-semibold tabular-nums">{listing.price === null ? t('results.price_unknown') : formatMoney(listing.price, locale)}</span>
            <StatusBadge tone={grade === 'avoid' ? 'danger' : grade === 'pricey' ? 'warning' : grade === 'good' || grade === 'steal' ? 'positive' : 'neutral'}>{heading}</StatusBadge>
            {listing.last_drop_amount !== null && listing.last_drop_amount > 0 && <StatusBadge tone="positive">{t('results.card.drop', { amount: formatMoney(listing.last_drop_amount, locale) })}</StatusBadge>}
            {isOverBudget(finding) && <StatusBadge tone="warning">{t('results.card.over_budget')}</StatusBadge>}
            {listing.sale_status === 'sold' && <StatusBadge>{t('results.card.sold')}</StatusBadge>}
            {listing.sale_status === 'offline' && <StatusBadge>{t('results.card.offline')}</StatusBadge>}
          </div>
          {percent !== null && <p className="text-body text-text-label">{t(percent >= 0 ? 'results.card.lower' : 'results.card.higher', { percent: Math.abs(percent) })}</p>}
          {computed && finding.fair_price !== null && finding.fair_sample !== null && <p className="text-body text-text-secondary">
            {t('results.card.fair_price', { price: formatMoney(finding.fair_price, locale), count: finding.fair_sample })}
            {finding.fair_basis && <> · {message(finding.fair_basis)}</>}
          </p>}
          <div className="flex flex-wrap gap-sm">
            {finding.item_specs?.config_key === '*' ? <StatusBadge>{t('results.card.no_config')}</StatusBadge>
              : finding.item_specs?.specs.map((spec) => <StatusBadge key={spec.name}>{`${spec.name}: ${spec.value ?? t('results.card.config_unknown')}`}</StatusBadge>)}
            {!finding.item_specs && finding.task.judge_mode === 'ai' && <StatusBadge>{t('results.card.config_unknown')}</StatusBadge>}
            {finding.matched_keywords.map((word) => <StatusBadge key={word} tone="accent">{word}</StatusBadge>)}
          </div>
          {finding.reason && <p className="break-words text-body text-text-label"><StoredMessage message={finding.reason} /></p>}
          <div className="flex flex-wrap gap-sm">
            {risks.filter(({ status, risk }) => status === 'risk' || status === 'suspect' ||
              status === 'unknown' && finding.questions.some((question) => question.key === risk.id)).map(({ risk, status, source }) => {
              const nameKey = `analysis.risk_name.${risk.id}`;
              const name = i18n.exists(nameKey) ? message({ code: nameKey, args: {} }) : risk.name ?? risk.id;
              return <StatusBadge key={risk.id} tone={status === 'risk' && source !== 'image' ? 'danger' : status === 'unknown' ? 'neutral' : 'warning'}>
                {t(status === 'unknown' ? 'results.card.confirm' : source === 'image' ? 'results.card.image' : status === 'suspect' ? 'results.card.suspect' : 'results.card.risk', { name })}
              </StatusBadge>;
            })}
            {finding.analysis?.extra_findings.map((extra, index) => <StatusBadge key={`${extra.name}-${index}`} tone={extra.source === 'image' ? 'warning' : 'danger'}>
              {t(extra.source === 'image' ? 'results.card.image' : 'results.card.risk', { name: extra.name })}
            </StatusBadge>)}
            {finding.questions.length > 0 && <StatusBadge>{t('results.card.question_count', { count: finding.questions.length })}</StatusBadge>}
          </div>
          <p className="break-words text-body text-text-label">
            {sellerKind === 'personal' ? t('results.card.seller_personal') : sellerKind === 'dealer_suspect' ? t('results.card.seller_suspect') : t('results.card.seller_unknown')}
            {' · '}<span title={registrationTitle}>{formatSellerRegistration(registrationDays, locale)}</span>
            {finding.seller?.seller_credit && <> · {finding.seller.seller_credit}</>}
            {sellerReviewSummary && <> · {sellerReviewSummary}</>}
          </p>
          <div className="flex flex-wrap gap-x-lg gap-y-sm text-caption text-text-secondary">
            <span>{listing.published_at === null ? t('results.published_unknown') : t('results.card.published', { time: resultTime(listing.published_at, locale, timeZone) })}</span>
            <span>{t('results.card.found', { time: resultTime(finding.found_at, locale, timeZone) })}</span>
            <span>{finding.task.name}</span>
          </div>
          <NotificationStatus finding={finding} timeZone={timeZone} />
          <div className="flex flex-wrap items-center gap-sm">
            <Button disabled={busy} onClick={() => onAction('favorite')}>{t(listing.is_favorite ? 'results.action.unfavorite' : 'results.action.favorite')}</Button>
            <CopyButton text={link ?? ''} disabled={!link} label={t('results.action.copy')} copiedLabel={t('results.action.copied')} errorLabel={t('results.action.copy_failed')} />
            {link && <a href={link} target="_blank" rel="noopener noreferrer" className="inline-flex min-h-control items-center rounded-control px-md text-body text-accent-text underline underline-offset-4 focus-visible:outline focus-visible:outline-2 focus-visible:outline-focus">{t('results.action.open')}</a>}
            <ActionMenu label={t('results.action.more')} items={[
              { id: 'reanalyze', label: t('results.action.reanalyze'), disabled: busy, onSelect: () => onAction('reanalyze') },
              { id: 'dismiss', label: t(listing.is_dismissed ? 'results.action.restore' : 'results.action.dismiss'), disabled: busy, onSelect: () => onAction(listing.is_dismissed ? 'restore' : 'dismiss') },
              { id: 'block', label: t('results.action.block'), disabled: busy || !listing.seller_id, onSelect: () => onAction('block') },
              { id: 'unread', label: t('results.action.unread'), disabled: busy || !finding.is_read, onSelect: () => onAction('unread') },
              { id: 'delete', label: t('results.action.delete'), disabled: busy, destructive: true, onSelect: () => onAction('delete') },
            ]} />
          </div>
        </div>
      </div>
    </article>
  );
}
