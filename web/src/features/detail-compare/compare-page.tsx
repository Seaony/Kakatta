import type { ReactNode } from 'react';
import { Link } from '@tanstack/react-router';
import { useQueries, useQuery } from '@tanstack/react-query';
import { apiData, useApi, useApiError, useMessageText, useTimeZone } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import { InlineMessage, StatePanel, StatusBadge } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { formatSellerRegistration } from '../../i18n/format';
import { StoredMessage } from '../../i18n/stored-message';
import { listingThumbnailURL } from '../results/model';
import { profileFields, type ComparePageProps, type Finding } from './data';
import { AnalysisWarnings, attitudeKeys, checkKeys, Evidence, Money, ScoreBreakdown, ScoreLabel, DateText } from './presentation';

export function ComparePage({ ids, timeZone: suppliedTimeZone }: ComparePageProps) {
  const { t, locale } = useTranslation();
  const api = useApi();
  const errorText = useApiError();
  const message = useMessageText();
  const rootTimeZone = useTimeZone();
  const timeZone = suppliedTimeZone ?? rootTimeZone;
  const valid = ids.length >= 2 && ids.length <= 4 && new Set(ids).size === ids.length && ids.every((id) => Number.isSafeInteger(id) && id > 0);
  const queries = useQueries({ queries: valid ? ids.map((findingId) => ({
    queryKey: apiKey('findings', 'detail', findingId, {}),
    queryFn: async ({ signal }: { signal: AbortSignal }) => apiData(await api.GET('/api/v1/findings/{finding_id}', { params: { path: { finding_id: findingId } }, signal })),
  })) : [] });
  const dictionary = useQuery({
    queryKey: apiKey('risk-dictionary'), enabled: valid,
    queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/risk-dictionary', { signal })),
  });
  if (!valid) return <StatePanel kind="empty" title={t('web.compare.selection_range')} action={<Link to="/results" className="inline-flex min-h-control items-center text-accent-text underline underline-offset-4">{t('web.detail.back_to_results')}</Link>} />;
  const findings = queries.map((query) => query.data);
  const loaded = findings.filter((finding): finding is Finding => finding !== undefined);
  const sameTask = loaded.length === ids.length && new Set(loaded.map((finding) => finding.task_id)).size === 1;
  const sameConfiguration = sameTask && loaded.every((finding) => finding.item_specs?.config_key !== null && finding.item_specs?.config_key !== undefined && finding.item_specs?.config_key === loaded[0]?.item_specs?.config_key && finding.item_specs?.brand_new === loaded[0]?.item_specs?.brand_new);
  const specNames = Array.from(new Set(loaded.flatMap((finding) => profileFields(finding.task.profile)?.specs.map((spec) => spec.name) ?? finding.item_specs?.specs.map((spec) => spec.name) ?? [])));
  const riskRows = new Map<string, { key: string; taskId: number; name: string }>();
  for (const finding of loaded) for (const risk of profileFields(finding.task.profile)?.risks ?? []) {
    const known = dictionary.data?.some((entry) => entry.id === risk.id) === true;
    const identity = known ? risk.id : `${finding.task_id}:${risk.id}`;
    if (!riskRows.has(identity)) riskRows.set(identity, { key: risk.id, taskId: known ? 0 : finding.task_id, name: known ? message({ code: `analysis.risk_name.${risk.id}`, args: {} }) : risk.name ?? risk.id });
  }
  const scores = loaded.map((finding) => finding.decision === 'scored' ? finding.score : null);
  const prices = loaded.map((finding) => finding.listing.price);
  return <article className="min-w-0 space-y-xl" aria-labelledby="compare-title">
    <header className="space-y-md"><Link to="/results" className="inline-flex min-h-control items-center text-body text-accent-text underline underline-offset-4">{t('web.detail.back_to_results')}</Link><h1 id="compare-title" className="text-page font-semibold">{t('web.compare.title')}</h1><p id="compare-scroll-hint" className="text-body text-text-secondary">{t('web.compare.scroll_hint')}</p></header>
    {!sameTask && loaded.length > 1 && <InlineMessage>{t('web.compare.different_tasks')}</InlineMessage>}
    <div className="max-w-full overflow-x-auto overscroll-x-contain rounded-control border border-border" tabIndex={0} role="region" aria-label={t('web.compare.title')} aria-describedby="compare-scroll-hint">
      <table className="w-full border-separate border-spacing-0 text-left text-body">
        <caption className="sr-only">{t('web.compare.title')}</caption>
        <thead><tr><th scope="col" className="sticky left-0 z-10 min-w-[8rem] bg-raised p-md align-top">{t('web.detail.title')}</th>{queries.map((query, index) => {
          const thumbnail = query.data && listingThumbnailURL(query.data.listing, query.data.id);
          return <th scope="col" key={ids[index]} className="min-w-[16rem] max-w-sm border-l border-border bg-raised p-lg align-top font-normal">
            {query.data ? <div className="space-y-md">
              {thumbnail && <img key={query.data.listing.thumbnail_url} src={thumbnail} referrerPolicy="no-referrer" alt="" onError={(event) => { event.currentTarget.hidden = true; }} className="h-[8rem] w-full rounded-thumb bg-inset object-contain" />}
              <h2 className="break-words font-semibold">{query.data.listing.title}</h2><p className="break-words text-caption text-text-secondary">{query.data.task.name}</p>
              <Link to="/results" search={{ task_id: query.data.task_id, finding_id: query.data.id }} className="inline-flex min-h-control items-center text-accent-text underline underline-offset-4">{t('web.compare.view_detail')}</Link>
            </div> : query.isPending ? <StatePanel kind="pending" label={t('common.loading')} /> : <StatePanel kind="error" title={t('web.compare.unavailable_column')} description={errorText(query.error)} retry={{ label: t('common.retry'), run: () => { void query.refetch(); } }} />}
          </th>;
        })}</tr></thead>
        <tbody>
          <CompareRow ids={ids} findings={findings} label={t('web.compare.price')} best={sameConfiguration ? (finding) => bestNumber(finding.listing.price, prices, 'lower') : undefined} render={(finding) => <div className="space-y-sm"><p className="text-section font-semibold"><Money value={finding.listing.price} /></p>{(finding.listing.sale_status === 'sold' || finding.listing.sale_status === 'offline') && <StatusBadge>{t(`web.detail.sale.${finding.listing.sale_status}`)}</StatusBadge>}</div>} />
          <CompareRow ids={ids} findings={findings} label={t('web.detail.score_market_basis')} render={(finding) => <div className="space-y-sm"><Money value={finding.fair_price} />{finding.fair_sample !== null && <p>{t('web.detail.sample_count', { count: finding.fair_sample })}</p>}{finding.fair_basis && <p className="break-words text-text-secondary"><StoredMessage message={finding.fair_basis} /></p>}{finding.score_breakdown?.base && <p className="tabular-nums">{new Intl.NumberFormat(locale, { style: 'percent', maximumFractionDigits: 1 }).format(finding.score_breakdown.base.d)}</p>}</div>} />
          <CompareRow ids={ids} findings={findings} label={t('web.detail.market_current')} render={(finding) => finding.market_comparison ? <div className="space-y-sm"><Money value={finding.market_comparison.fair.price} /><p>{message(finding.market_comparison.fair.basis)}</p><p>{t('web.detail.sample_count', { count: finding.market_comparison.fair.sample })}</p><DateText value={finding.market_comparison.as_of} {...(timeZone ? { timeZone } : {})} /></div> : t('web.detail.no_fair_price')} />
          {specNames.map((name) => <CompareRow key={name} ids={ids} findings={findings} label={name} render={(finding) => finding.item_specs?.specs.find((spec) => spec.name === name)?.value ?? t('web.detail.unknown')} />)}
          <CompareRow ids={ids} findings={findings} label={t('web.detail.specs')} render={(finding) => <p>{finding.item_specs?.config_key === '*' ? t('web.detail.no_configuration') : finding.item_specs?.config_key ?? t('web.detail.unknown')} · {t(finding.item_specs?.brand_new === true ? 'web.detail.brand_new' : finding.item_specs?.brand_new === false ? 'web.detail.used' : 'web.detail.condition_unknown')}</p>} />
          {loaded.some((finding) => finding.analysis_warnings.length > 0) && <CompareRow ids={ids} findings={findings} label={t('web.detail.risks_title')} render={(finding) => <AnalysisWarnings finding={finding} />} />}
          {Array.from(riskRows).map(([identity, row]) => <CompareRow key={identity} ids={ids} findings={findings} label={row.name} render={(finding) => {
            if (row.taskId !== 0 && row.taskId !== finding.task_id) return t('web.compare.incomparable');
            const risk = profileFields(finding.task.profile)?.risks.find((entry) => entry.id === row.key);
            if (!risk) return t('web.compare.incomparable');
            const check = finding.checks.find((entry) => entry.key === row.key);
            return <div className="space-y-md"><p className="text-caption text-text-secondary">{t(attitudeKeys[risk.attitude])}</p><p>{t('web.detail.current_conclusion')} · {t(checkKeys[check?.status ?? 'unknown'])}</p><Evidence source={check?.source ?? null} evidence={check?.evidence ?? null} /></div>;
          }} />)}
          <CompareRow ids={ids} findings={findings} label={t('web.detail.extra_title')} render={(finding) => <div className="space-y-lg">{finding.analysis?.extra_findings.length ? finding.analysis.extra_findings.map((extra, index) => <div key={index} className="space-y-sm"><h3 className="font-medium">{extra.name}</h3><Evidence source={extra.source} evidence={extra.evidence} /></div>) : <p className="text-text-secondary">{t('web.detail.extra_empty')}</p>}</div>} />
          <CompareRow ids={ids} findings={findings} label={t('web.compare.seller')} render={(finding) => <div className="space-y-sm"><p>{finding.seller?.nick ?? finding.listing.seller_nick ?? t('web.detail.unknown')}</p><p>{t(`web.detail.seller_type.${finding.seller_assessment?.type ?? finding.seller_type ?? 'unknown'}`)}</p><p>{finding.seller?.seller_credit ?? t('web.detail.unknown')}</p><p>{formatSellerRegistration(finding.listing.seller_registration_days, locale)}</p><p className="text-caption text-text-secondary">{t('web.detail.seller_sampling')}</p>{finding.seller_assessment?.reasons.map((reason, index) => <p key={index} className="break-words"><StoredMessage message={reason} /></p>)}{finding.analysis?.seller_signals.map((signal, index) => <p key={index} className="break-words">{signal.evidence}</p>)}</div>} />
          <CompareRow ids={ids} findings={findings} label={t('web.compare.score')} best={sameTask ? (finding) => finding.decision === 'scored' && bestNumber(finding.score, scores, 'higher') : undefined} render={(finding) => <ScoreLabel finding={finding} />} />
          <CompareRow ids={ids} findings={findings} label={t('web.compare.breakdown')} render={(finding) => <ScoreBreakdown finding={finding} />} />
        </tbody>
      </table>
    </div>
    <p className="text-caption text-text-secondary">{t('web.compare.incomparable')}</p>
  </article>;
}

function CompareRow({ ids, findings, label, render, best }: { ids: readonly number[]; findings: (Finding | undefined)[]; label: string; render(finding: Finding): ReactNode; best?: ((finding: Finding) => boolean) | undefined }) {
  const { t } = useTranslation();
  return <tr><th scope="row" className="sticky left-0 z-10 min-w-[8rem] border-t border-border bg-raised p-md align-top font-medium">{label}</th>{ids.map((id, index) => {
    const finding = findings[index];
    const highlighted = finding && best?.(finding);
    return <td key={id} className={`min-w-[16rem] max-w-sm border-l border-t border-border p-lg align-top ${highlighted ? 'bg-accent-fill' : 'bg-raised'}`}>{finding ? <div className="min-w-0 space-y-md break-words">{highlighted && <span className="text-caption font-medium text-accent-text">{t('web.compare.best_value')}</span>}{render(finding)}</div> : t('web.detail.unknown')}</td>;
  })}</tr>;
}

function bestNumber(value: number | null, values: (number | null)[], direction: 'lower' | 'higher') {
  const known = values.filter((candidate): candidate is number => candidate !== null);
  if (value === null || known.length < 2) return false;
  return value === (direction === 'lower' ? Math.min(...known) : Math.max(...known));
}
