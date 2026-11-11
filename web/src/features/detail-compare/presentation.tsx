import { useState, type ReactNode } from 'react';
import { useApiError } from '../../api/hooks';
import type { Schema } from '../../api/types';
import { Button, Dialog, StatePanel, StatusBadge, CopyButton, InlineMessage } from '../../components/ui';
import { useTranslation, type TranslationKey } from '../../i18n';
import { formatDateTime, formatMoney, formatSellerName, formatSellerRegistration } from '../../i18n/format';
import { formatStoredMessage, StoredMessage } from '../../i18n/stored-message';
import { profileFields, safeExternalURL, usePriceHistory, type Finding } from './data';
import { listingThumbnailURL } from '../results/model';
import { NotificationStatus } from '../results/notification-status';

export const decisionKeys = {
  pending: 'web.detail.state.pending', scored: 'web.detail.state.scored', matched: 'web.detail.state.matched',
  rejected: 'web.detail.state.rejected', failed: 'web.detail.state.failed', skipped: 'web.detail.state.skipped',
} as const;
export const checkKeys = {
  ok: 'web.detail.status.ok', risk: 'web.detail.status.risk', suspect: 'web.detail.status.suspect', unknown: 'web.detail.status.unknown',
} as const;
export const attitudeKeys = {
  confirm: 'web.detail.attitude.confirm', reject: 'web.detail.attitude.reject', mind: 'web.detail.attitude.mind', ignore: 'web.detail.attitude.ignore',
} as const;
const sourceKeys: Readonly<Record<string, TranslationKey>> = {
  text: 'web.detail.source.text', image: 'web.detail.source.image', program: 'web.detail.source.program', user: 'web.detail.source.user',
};

export function Section({ title, children, id }: { title: string; children: ReactNode; id?: string }) {
  return <section {...(id ? { id } : {})} className="min-w-0 space-y-lg border-t border-border pt-xl">
    <h2 className="text-section font-semibold text-text-primary">{title}</h2>{children}
  </section>;
}

export function Money({ value }: { value: number | null }) {
  const { t, locale } = useTranslation();
  return <span className="tabular-nums">{value === null ? t('web.detail.price_unknown') : formatMoney(value, locale)}</span>;
}

export function DateText({ value, timeZone }: { value: number | null; timeZone?: string }) {
  const { t, locale } = useTranslation();
  if (value === null) return <span>{t('web.detail.unknown')}</span>;
  const iso = new Date(value).toISOString();
  return <time dateTime={iso} className="tabular-nums">{timeZone ? formatDateTime(value, locale, timeZone) : iso}</time>;
}

export function ScoreLabel({ finding }: { finding: Finding }) {
  const { t } = useTranslation();
  if (finding.decision !== 'scored') return <StatusBadge tone={finding.decision === 'failed' ? 'danger' : 'neutral'}>{t(decisionKeys[finding.decision])}</StatusBadge>;
  const tone = finding.grade === 'avoid' ? 'danger' : finding.grade === 'pricey' ? 'warning' : finding.grade === 'good' || finding.grade === 'steal' ? 'positive' : 'neutral';
  return <span className="inline-flex flex-wrap items-center gap-sm">
    {finding.score !== null && <strong className="text-page tabular-nums">{finding.score}</strong>}
    <StatusBadge tone={tone}>{finding.grade ? t(`analysis.grade.${finding.grade}`) : t('web.detail.no_fair_price')}</StatusBadge>
    {finding.score === null && finding.grade !== null && <span>{t('web.detail.no_fair_price')}</span>}
  </span>;
}

export function ScoreBreakdown({ finding }: { finding: Finding }) {
  const { t, locale } = useTranslation();
  const breakdown = finding.decision === 'scored' ? finding.score_breakdown : null;
  const profile = profileFields(finding.task.profile);
  const facts = finding.decision === 'scored' || finding.decision === 'rejected' && finding.decided_layer === 3 ? finding.analysis : null;
  const number = new Intl.NumberFormat(locale, { maximumFractionDigits: 1, signDisplay: 'exceptZero' });
  return <div className="min-w-0 space-y-lg">
    <ScoreLabel finding={finding} />
    {finding.reason && <p className="break-words text-body text-text-body"><StoredMessage message={finding.reason} /></p>}
    {finding.title_screen_reason && finding.decision === 'rejected' && <p className="break-words text-body text-text-secondary">{finding.title_screen_reason}</p>}
    {finding.matched_keywords.length > 0 && <p className="break-words text-body">{finding.matched_keywords.join(' · ')}</p>}
    {breakdown && <>
      <div className="grid min-w-0 gap-md sm:grid-cols-2">
        <div><h3 className="text-body font-medium">{t('web.detail.base_score')}</h3><p className="mt-sm text-section tabular-nums">{breakdown.base ? new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(breakdown.base.score) : t('web.detail.no_fair_price')}</p>
          {breakdown.base && <p className="mt-sm text-body text-text-secondary">{new Intl.NumberFormat(locale, { style: 'percent', maximumFractionDigits: 1 }).format(breakdown.base.d)}</p>}
        </div>
        <div><h3 className="text-body font-medium">{t('web.detail.score_market_basis')}</h3><p className="mt-sm text-section"><Money value={finding.fair_price} /></p>
          {finding.fair_sample !== null && <p className="mt-sm text-body">{t('web.detail.sample_count', { count: finding.fair_sample })}</p>}
          {finding.fair_basis && <p className="mt-sm text-body text-text-secondary"><StoredMessage message={finding.fair_basis} /></p>}
          <p className="mt-sm text-caption text-text-secondary">{t('web.detail.asking_price_note')}</p>
        </div>
      </div>
      <div className="space-y-sm"><h3 className="text-body font-medium">{t('web.detail.rule_adjustments')}</h3>
        <ul className="divide-y divide-border">{breakdown.items.map((item) => <li key={item.id} className="flex min-w-0 items-start justify-between gap-md py-md">
          <div className="min-w-0"><p className="break-words text-body"><StoredMessage message={item.reason} /></p><span className="text-caption text-text-secondary">{t(item.source === 'user' ? 'web.detail.manual_conclusion' : 'web.detail.rule')}</span></div>
          <strong className="shrink-0 text-body tabular-nums">{number.format(item.value)}</strong>
        </li>)}</ul>
      </div>
      <div className="space-y-sm"><h3 className="text-body font-medium">{t('web.detail.ai_adjustment')} <span className="ml-sm tabular-nums">{number.format(breakdown.ai.value)}</span></h3><p className="whitespace-pre-wrap break-words text-body text-text-body">{breakdown.ai.reason || t('web.detail.no_analysis')}</p></div>
      {breakdown.caps.length > 0 && <div className="space-y-sm"><h3 className="text-body font-medium">{t('web.detail.caps')}</h3><ul className="space-y-sm">{breakdown.caps.map((cap, index) => <li key={index} className="break-words text-body text-warning">{t('web.detail.cap', { limit: cap.limit })} · <StoredMessage message={cap.reason} /></li>)}</ul></div>}
      {breakdown.gates.length > 0 && <div className="space-y-sm"><h3 className="text-body font-medium">{t('web.detail.gates')}</h3><ul className="space-y-sm">{breakdown.gates.map((gate, index) => <li key={index} className="break-words text-body text-danger"><StoredMessage message={gate} /></li>)}</ul></div>}
    </>}
    {facts && profile && (['musts', 'bonuses'] as const).map((group) => profile[group].length > 0 && <div key={group} className="space-y-sm">
      <h3 className="text-body font-medium">{t(`tasks.profile.${group}`)}</h3>
      <ul className="divide-y divide-border">{profile[group].map((requirement) => {
        const fact = facts[group].find((item) => item.id === requirement.id && item.rev > 0 && item.rev === (requirement.rev === 0 ? 1 : requirement.rev));
        return <li key={requirement.id} className="min-w-0 space-y-md py-md">
          <div className="flex min-w-0 flex-wrap items-start justify-between gap-md"><h4 className="break-words text-body font-medium">{requirement.text}</h4><StatusBadge tone={fact?.status === 'unmet' ? 'danger' : 'neutral'}>{t(`web.detail.requirement.${fact?.status ?? 'unknown'}`)}</StatusBadge></div>
          <Evidence source={fact?.source ?? null} evidence={fact?.evidence ?? null} />
        </li>;
      })}</ul>
    </div>)}
  </div>;
}

export function AnalysisWarnings({ finding, linkChecks = false }: { finding: Finding; linkChecks?: boolean }) {
  const risks = profileFields(finding.task.profile)?.risks ?? [];
  if (finding.analysis_warnings.length === 0) return null;
  return <InlineMessage tone="warning"><ul className="space-y-sm">{finding.analysis_warnings.map((warning, index) => {
    const field = warning.args.field ?? warning.args.item;
    const risk = typeof field === 'string' ? risks.find((entry) => entry.id === field) : undefined;
    const content = <StoredMessage message={warning} />;
    return <li key={index}>{linkChecks && risk ? <a className="inline-flex min-h-control items-center underline underline-offset-4" href={`#finding-${finding.id}-risk-${encodeURIComponent(risk.id)}`}>{content}</a> : content}</li>;
  })}</ul></InlineMessage>;
}

export function Evidence({ source, evidence }: { source: string | null; evidence: string | null }) {
  const { t } = useTranslation();
  return <div className="space-y-sm">
    <StatusBadge tone={source === 'image' ? 'warning' : 'neutral'}>{t(source ? sourceKeys[source] ?? 'web.detail.source.unknown' : 'web.detail.source.unknown')}</StatusBadge>
    <p className="whitespace-pre-wrap break-words text-body text-text-body">{evidence ?? t('web.detail.evidence_missing')}</p>
  </div>;
}

export function Images({ listing, findingId }: { listing: Schema<'Listing'>; findingId: number }) {
  const { t } = useTranslation();
  const thumbnail = listingThumbnailURL(listing, findingId);
  const sources = listing.image_urls.length ? listing.image_urls.map(safeExternalURL).filter((url): url is string => Boolean(url)) : thumbnail ? [thumbnail] : [];
  const [index, setIndex] = useState(0);
  const [zoom, setZoom] = useState(false);
  const [failed, setFailed] = useState<string[]>([]);
  const current = sources[index] ?? sources[0];
  if (!current) return <div className="flex aspect-[4/3] w-full items-center justify-center rounded-card bg-inset p-lg"><p className="text-body text-text-secondary">{t('web.detail.image_empty')}</p></div>;
  const image = (large: boolean) => failed.includes(current)
    ? <p className="p-lg text-body text-text-secondary">{t('web.detail.image_failed')}</p>
    : <img src={current} referrerPolicy="no-referrer" alt={listing.title} onError={() => setFailed((items) => [...items, current])} className={large ? 'max-h-[75vh] w-full object-contain' : 'h-full w-full object-contain'} />;
  return <div className="min-w-0 space-y-md">
    <button type="button" aria-label={t('web.detail.zoom')} onClick={() => setZoom(true)} className="flex aspect-[4/3] w-full items-center justify-center overflow-hidden rounded-card bg-inset focus-visible:outline focus-visible:outline-2 focus-visible:outline-focus active:scale-[.99]">{image(false)}</button>
    <div className="flex max-w-full gap-sm overflow-x-auto pb-sm" role="group" aria-label={t('web.detail.images')}>
      {sources.map((url, n) => <Button key={url} variant={n === index ? 'primary' : 'secondary'} aria-pressed={n === index} aria-label={t('web.detail.image', { index: n + 1, count: sources.length })} onClick={() => setIndex(n)}>{n + 1}</Button>)}
    </div>
    {failed.includes(current) && <Button onClick={() => setFailed((items) => items.filter((url) => url !== current))}>{t('common.retry')}</Button>}
    <Dialog open={zoom} onOpenChange={setZoom} title={listing.title} closeLabel={t('common.close')}>{image(true)}</Dialog>
  </div>;
}

export function ContactQuestions({ questions }: { questions: Schema<'AnalysisContactQuestion'>[] }) {
  const { t, locale } = useTranslation();
  const visible = questions.slice(0, 5);
  return visible.length ? <div className="space-y-lg">
    <ol className="list-inside list-decimal space-y-md text-body">{visible.map((question) => <li key={question.key} className="break-words"><StoredMessage message={question.question} /></li>)}</ol>
    <CopyButton text={visible.map((question) => formatStoredMessage(question.question, locale).text).join('\n')} label={t('web.detail.contact_copy')} copiedLabel={t('web.detail.copy_success')} errorLabel={t('web.detail.copy_failed')} />
  </div> : <p className="text-body text-text-secondary">{t('web.detail.contact_empty')}</p>;
}

export function Deliveries({ finding, priceDrops = false, timeZone }: { finding: Finding; priceDrops?: boolean; timeZone?: string }) {
  const { t } = useTranslation();
  if (!priceDrops) return <NotificationStatus finding={finding} {...(timeZone ? { timeZone } : {})} />;
  const deliveries = finding.price_drop_deliveries ?? finding.deliveries.filter((delivery) => delivery.kind === 'price_drop');
  return <div className="space-y-md text-body">
    {deliveries.map((delivery) => <div key={delivery.id} className="min-w-0 space-y-sm border-b border-border pb-md">
      <p className="flex flex-wrap items-center gap-sm"><span>{delivery.channel_name ?? t('web.detail.notification_channel_unknown')}</span><StatusBadge tone={delivery.ok === false ? 'danger' : delivery.ok === true ? 'positive' : 'neutral'}>{t(delivery.ok === false ? 'web.detail.notification_failed' : delivery.ok === true ? 'web.detail.notification_sent' : 'web.detail.notification_unknown')}</StatusBadge><DateText value={delivery.at} {...(timeZone ? { timeZone } : {})} /></p>
      {delivery.kind === 'summary' && delivery.item_count !== null && <p>{t('web.detail.notification_grouped', { count: delivery.item_count })}</p>}
      {delivery.error && <p className="break-words text-danger"><StoredMessage message={delivery.error} /></p>}
    </div>)}
  </div>;
}

export function Seller({ finding, timeZone }: { finding: Finding; timeZone?: string }) {
  const { t, locale } = useTranslation();
  const seller = finding.seller;
  const type = finding.seller_assessment?.type ?? finding.seller_type ?? 'unknown';
  return <div className="space-y-lg">
    <p className="flex flex-wrap items-center gap-sm"><strong className="break-words">{formatSellerName(seller?.nick, locale, finding.listing.seller_nick)}</strong><StatusBadge tone={type === 'dealer_suspect' ? 'warning' : 'neutral'}>{t(`web.detail.seller_type.${type}`)}</StatusBadge></p>
    <p className="text-body">{formatSellerRegistration(finding.listing.seller_registration_days, locale)}</p>
    {finding.seller_assessment && <div className="space-y-sm"><h3 className="text-body font-medium">{t('web.detail.seller_assessment')}</h3><p className="text-caption text-text-secondary"><DateText value={finding.analyzed_at} {...(timeZone ? { timeZone } : {})} /></p><ul className="space-y-sm">{finding.seller_assessment.reasons.map((reason, index) => <li key={index} className="break-words text-body"><StoredMessage message={reason} /></li>)}</ul></div>}
    {seller ? <>
      <p className="text-caption text-text-secondary">{t('web.detail.seller_sampling')}</p>
      <dl className="grid grid-cols-1 gap-lg text-body sm:grid-cols-2">
        <div><dt className="text-text-secondary">{t('web.detail.seller_credit')}</dt><dd className="mt-sm">{seller.seller_credit ?? t('web.detail.unknown')}</dd></div>
        <div><dt className="text-text-secondary">{t('web.detail.buyer_credit')}</dt><dd className="mt-sm">{seller.buyer_credit ?? t('web.detail.unknown')}</dd></div>
        <div><dt className="text-text-secondary">{t('web.detail.seller_on_sale')}</dt><dd className="mt-sm tabular-nums">{seller.on_sale_count ?? t('web.detail.unknown')}</dd></div>
        <div><dt className="text-text-secondary">{t('web.detail.seller_sold')}</dt><dd className="mt-sm tabular-nums">{seller.sold_count ?? t('web.detail.unknown')}</dd></div>
        <div><dt className="text-text-secondary">{t('web.detail.seller_rates')}</dt><dd className="mt-sm tabular-nums">{seller.seller_good_rate === null ? t('web.detail.unknown') : new Intl.NumberFormat(locale, { style: 'percent', maximumFractionDigits: 1 }).format(seller.seller_good_rate)}</dd></div>
        <div><dt className="text-text-secondary">{t('web.detail.updated_at')}</dt><dd className="mt-sm"><DateText value={seller.fetched_at} {...(timeZone ? { timeZone } : {})} /></dd></div>
      </dl>
      {seller.introduction && <p className="whitespace-pre-wrap break-words text-body">{seller.introduction}</p>}
      <div className="space-y-md">
        <h3 className="text-body font-medium">{t('web.detail.reviews')}</h3>
        {seller.recent_rates === null ? <p className="text-body text-text-secondary">{t('web.detail.unknown')}</p>
          : seller.recent_rates.map((rate, index) => {
            const role = rate.role === 'seller' ? t('web.detail.review_as_seller') : rate.role === 'buyer' ? t('web.detail.review_as_buyer') : '';
            const kind = rate.kind === 'good' || rate.kind === 'neutral' || rate.kind === 'bad' ? t(`web.detail.review_${rate.kind}`) : '';
            const label = [role, kind].filter(Boolean).join(' · ');
            return <blockquote key={index} className="break-words border-b border-border pb-md text-body">
              <p>{rate.text ?? t('web.detail.unknown')}</p>
              {label && <footer className="mt-sm text-caption text-text-secondary">{label}</footer>}
            </blockquote>;
          })}
      </div>
    </> : <p className="text-body text-text-secondary">{t(finding.seller_assessment ? 'web.detail.seller_cache_missing' : 'analysis.seller_unavailable')}</p>}
    {finding.analysis?.seller_signals.map((signal, index) => <Evidence key={index} source="text" evidence={signal.evidence} />)}
  </div>;
}

export function PriceHistory({ finding, timeZone }: { finding: Finding; timeZone?: string }) {
  const { t } = useTranslation();
  const errorText = useApiError();
  const query = usePriceHistory(finding.item_id);
  const points = query.data?.pages.flatMap((page) => page.items) ?? [];
  return <div className="space-y-lg">
    <p className="text-body text-text-secondary">{t('web.detail.history_observed')}</p>
    {query.isPending && <StatePanel kind="pending" label={t('common.loading')} />}
    {query.error && <StatePanel kind="error" title={t('web.request_failed')} description={errorText(query.error)} retry={{ label: t('common.retry'), run: () => { void query.refetch(); } }} />}
    {!query.isPending && !query.error && points.length === 0 && <p className="text-body text-text-secondary">{t('web.detail.history_empty')}</p>}
    {points.length > 1 && <PricePlot points={points} />}
    {points.length > 0 && <div className="max-h-[24rem] overflow-auto rounded-control border border-border" tabIndex={0} role="region" aria-label={t('web.detail.price_history')}><table className="w-full text-left text-body"><thead className="sticky top-0 bg-raised"><tr><th scope="col" className="p-md">{t('web.detail.observed_at')}</th><th scope="col" className="p-md text-right">{t('web.detail.price')}</th></tr></thead><tbody>{points.map((point) => <tr key={point.id} className="border-t border-border"><td className="p-md"><DateText value={point.observed_at} {...(timeZone ? { timeZone } : {})} /></td><td className="p-md text-right"><Money value={point.price} /></td></tr>)}</tbody></table></div>}
    {query.hasNextPage && <Button busy={query.isFetchingNextPage} onClick={() => { void query.fetchNextPage(); }}>{t('web.detail.load_more')}</Button>}
    {finding.drop_notified_at !== null && <div className="space-y-sm text-body"><p>{t('web.detail.drop_notified_at')} · <DateText value={finding.drop_notified_at} {...(timeZone ? { timeZone } : {})} /></p><p>{t('web.detail.drop_reference_price')} · <Money value={finding.last_drop_notified_price ?? finding.recommended_price} /></p></div>}
    <Deliveries finding={finding} priceDrops {...(timeZone ? { timeZone } : {})} />
  </div>;
}

function PricePlot({ points }: { points: Schema<'PricePoint'>[] }) {
  const { t } = useTranslation();
  const prices = points.map((point) => point.price);
  const min = prices.reduce((lowest, price) => Math.min(lowest, price), Infinity);
  const max = prices.reduce((highest, price) => Math.max(highest, price), -Infinity);
  const first = points[0]?.observed_at ?? 0, last = points[points.length - 1]?.observed_at ?? first;
  const coordinates = points.map((point) => `${16 + (point.observed_at - first) / Math.max(1, last - first) * 608},${148 - (point.price - min) / Math.max(1, max - min) * 124}`).join(' ');
  return <svg viewBox="0 0 640 172" className="w-full text-accent-text" role="img" aria-label={t('web.detail.price_history')}><polyline points={coordinates} fill="none" stroke="currentColor" strokeWidth="2" vectorEffect="non-scaling-stroke" /></svg>;
}
