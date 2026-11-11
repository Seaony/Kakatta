import { useEffect, useRef, useState } from 'react';
import { Link } from '@tanstack/react-router';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiData, useApi, useApiError, useMessageText, useTimeZone } from '../../api/hooks';
import { ApiError } from '../../api/errors';
import { apiKey } from '../../api/keys';
import { Button, ConfirmDialog, CopyButton, InlineMessage, Skeleton, StatePanel, StatusBadge } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { formatMoney } from '../../i18n/format';
import { invalidateResults, profileFields, safeExternalURL, useFinding, useMarketSamples, type Finding, type FindingDetailProps } from './data';
import { ExtraFindings, Risks, SpecsEditor } from './editors';
import { AnalysisWarnings, ContactQuestions, DateText, Deliveries, Images, Money, PriceHistory, ScoreBreakdown, Section, Seller } from './presentation';

export function FindingDetail(props: FindingDetailProps) {
  const { t } = useTranslation();
  const errorText = useApiError();
  const query = useFinding(props.findingId, props.query);
  const rootTimeZone = useTimeZone();
  const timeZone = props.timeZone ?? rootTimeZone;
  if (!Number.isSafeInteger(props.findingId) || props.findingId < 1) return <StatePanel kind="empty" title={t('web.detail.unavailable')} />;
  if (query.isPending) return <StatePanel kind="pending" label={t('common.loading')}><Skeleton className="h-[16rem] w-full" /><Skeleton className="h-[2.5rem] w-2/3" /></StatePanel>;
  if (!query.data) return <StatePanel kind="error" title={t('web.detail.unavailable')} description={errorText(query.error)} retry={{ label: t('common.retry'), run: () => { void query.refetch(); } }} />;
  return <div className="min-w-0 space-y-lg">
    {query.error && <InlineMessage tone="warning" action={<Button onClick={() => { void query.refetch(); }}>{t('common.retry')}</Button>}>{t('web.detail.refresh_failed')} {errorText(query.error)}</InlineMessage>}
    <DetailContent key={query.data.id} {...props} finding={query.data} {...(timeZone ? { timeZone } : {})} refreshing={query.isFetching} onRefresh={() => { void query.refetch(); }} />
  </div>;
}

function DetailContent({ finding, onClose, onNavigate, refreshing, onRefresh, timeZone }: FindingDetailProps & { finding: Finding; refreshing: boolean; onRefresh(): void }) {
  const { t } = useTranslation();
  const api = useApi();
  const message = useMessageText();
  const errorText = useApiError();
  const client = useQueryClient();
  const opened = useRef(false);
  const [specsOpen, setSpecsOpen] = useState(false);
  const [confirmAction, setConfirmAction] = useState<'dismiss' | 'block' | null>(null);
  const [reanalysisOpen, setReanalysisOpen] = useState(false);
  const [feedback, setFeedback] = useState<string | null>(null);
  const dictionary = useQuery({
    queryKey: apiKey('risk-dictionary'),
    queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/risk-dictionary', { signal })),
  });
  const markRead = useMutation({
    mutationFn: async (read: boolean) => apiData(await api.PATCH('/api/v1/findings/{finding_id}', { params: { path: { finding_id: finding.id } }, body: { is_read: read } })),
    onSuccess: async (_result, read) => { if (!read) setFeedback(t('web.detail.saved')); await client.invalidateQueries({ queryKey: apiKey('findings') }); },
  });
  useEffect(() => {
    if (opened.current) return;
    opened.current = true;
    if (!finding.is_read) markRead.mutate(true);
  }, [finding.is_read, markRead.mutate]);

  const favorite = useMutation({
    mutationFn: async () => apiData(await api.PATCH('/api/v1/listings/{item_id}', { params: { path: { item_id: finding.item_id } }, body: { is_favorite: !finding.listing.is_favorite } })),
    onSuccess: async () => { setFeedback(t('web.detail.saved')); await invalidateResults(client); },
  });
  const dismiss = useMutation({
    mutationFn: async (dismissed: boolean) => apiData(await api.PATCH('/api/v1/listings/{item_id}', { params: { path: { item_id: finding.item_id } }, body: { is_dismissed: dismissed } })),
    onSuccess: async () => { setConfirmAction(null); setFeedback(t('web.detail.saved')); await invalidateResults(client); },
  });
  const block = useMutation({
    mutationFn: async () => {
      const sellerId = finding.listing.seller_id;
      if (!sellerId) throw new ApiError(0, { code: 'web.request_failed', args: {} });
      return apiData(await api.PUT('/api/v1/blocked-sellers/{seller_id}', { params: { path: { seller_id: sellerId } }, body: { nick: finding.listing.seller_nick } }));
    },
    onSuccess: async () => { setConfirmAction(null); setFeedback(t('web.detail.saved')); await invalidateResults(client); },
  });
  const preview = useMutation({
    mutationFn: async () => apiData(await api.POST('/api/v1/findings/reanalysis-preview', { body: { ids: [finding.id] } })),
    onSuccess: () => { reanalyze.reset(); setReanalysisOpen(true); },
  });
  const reanalyze = useMutation({
    mutationFn: async () => apiData(await api.POST('/api/v1/findings/{finding_id}/reanalyze', { params: { path: { finding_id: finding.id } } })),
    onSuccess: async (result) => {
      setReanalysisOpen(false);
      const item = result.items.find((entry) => entry.finding_id === finding.id);
      setFeedback(item ? item.reason ? message(item.reason) : t(item.status === 'queued' ? 'web.detail.reanalysis_queued' : item.status === 'cached' ? 'web.detail.reanalyze_cached' : 'web.detail.reanalyze_rejected') : t('web.request_failed'));
      await invalidateResults(client);
    },
  });
  const run = useMutation({
    mutationFn: async () => apiData(await api.POST('/api/v1/tasks/{task_id}/run', { params: { path: { task_id: finding.task_id } } })),
    onSuccess: async (result) => { setFeedback(t(result.status === 'started' ? 'web.detail.run_started' : 'web.detail.run_queued')); await invalidateResults(client); },
  });
  const link = safeExternalURL(finding.listing.desktop_url);
  const actionError = favorite.error ?? dismiss.error ?? block.error ?? preview.error ?? run.error;
  const profile = profileFields(finding.task.profile);
  const actionsBusy = favorite.isPending || dismiss.isPending || block.isPending;
  return <article className="min-w-0 space-y-xxl" aria-label={t('web.detail.title')}>
    <nav className="flex flex-wrap items-center justify-between gap-md" aria-label={t('web.detail.title')}>
      {onClose ? <Button onClick={onClose}>{t('web.detail.back_to_results')}</Button> : <Link to="/results" className="inline-flex min-h-control items-center rounded-control px-md text-body text-accent-text underline underline-offset-4">{t('web.detail.back_to_results')}</Link>}
      <div className="flex flex-wrap gap-sm">
        <Neighbor id={finding.previous_id} taskId={finding.task_id} label={t('web.detail.previous')} {...(onNavigate ? { onNavigate } : {})} />
        <Neighbor id={finding.next_id} taskId={finding.task_id} label={t('web.detail.next')} {...(onNavigate ? { onNavigate } : {})} />
      </div>
    </nav>
    {feedback && <InlineMessage tone="info">{feedback}</InlineMessage>}
    {actionError && <InlineMessage tone="danger">{errorText(actionError)}{actionError instanceof ApiError && actionError.requestID && <p className="mt-sm break-all text-caption">{actionError.requestID}</p>}{actionError instanceof ApiError && typeof actionError.reason.args.next_allowed_at === 'number' && <p className="mt-sm text-body">{t('web.detail.next_allowed_at')} · <DateText value={actionError.reason.args.next_allowed_at} {...(timeZone ? { timeZone } : {})} /></p>}</InlineMessage>}
    {markRead.error && <InlineMessage tone="warning" action={<Button busy={markRead.isPending} onClick={() => markRead.mutate(markRead.variables ?? true)}>{t('common.retry')}</Button>}>{markRead.variables === false ? `${t('web.detail.mark_unread')} · ${errorText(markRead.error)}` : t('web.detail.mark_read_failed')}</InlineMessage>}

    <section className="min-w-0 space-y-lg" aria-label={t('web.detail.price')}>
      <h1 className="break-words text-page font-semibold leading-tight">{finding.listing.title}</h1>
      <div className="grid min-w-0 gap-xl lg:grid-cols-2">
        <Images listing={finding.listing} findingId={finding.id} />
        <div className="min-w-0 space-y-lg">
          <p className="text-page font-semibold"><Money value={finding.listing.price} /></p>
          {finding.listing.price === null && finding.listing.price_text && <p className="break-words text-body">{finding.listing.price_text}</p>}
          {finding.listing.original_price_text && <p className="text-body text-text-secondary">{t('web.detail.original_price')} · {finding.listing.original_price_text}</p>}
          <div className="flex flex-wrap gap-sm">
            {finding.listing.sale_status === 'sold' || finding.listing.sale_status === 'offline' ? <StatusBadge>{t(`web.detail.sale.${finding.listing.sale_status}`)}</StatusBadge> : null}
            {finding.item_specs?.specs.map((spec) => <StatusBadge key={spec.name} className="[overflow-wrap:anywhere]">{`${spec.name}: ${spec.value ?? t('web.detail.unknown')}`}</StatusBadge>)}
            {finding.item_specs?.config_key === '*' && <StatusBadge>{t('web.detail.no_configuration')}</StatusBadge>}
            <StatusBadge>{t(finding.item_specs?.brand_new === true ? 'web.detail.brand_new' : finding.item_specs?.brand_new === false ? 'web.detail.used' : 'web.detail.condition_unknown')}</StatusBadge>
            {finding.listing.tags.map((tag) => <StatusBadge key={tag}>{tag}</StatusBadge>)}
          </div>
          <dl className="space-y-md text-body">
            <div><dt className="text-text-secondary">{t('web.detail.published_at')}</dt><dd className="mt-sm"><DateText value={finding.listing.published_at} {...(timeZone ? { timeZone } : {})} /></dd></div>
            <div><dt className="text-text-secondary">{t('web.detail.found_at')}</dt><dd className="mt-sm"><DateText value={finding.found_at} {...(timeZone ? { timeZone } : {})} /></dd></div>
            {finding.listing.area && <div><dt className="text-text-secondary">{t('web.detail.area')}</dt><dd className="mt-sm break-words">{finding.listing.area}</dd></div>}
          </dl>
          {finding.listing.last_drop_amount !== null && <p className="text-body text-positive">{t('web.detail.price_drop')} <Money value={finding.listing.last_drop_amount} /></p>}
          <div className="flex flex-wrap items-start gap-sm">
            {link && <a href={link} target="_blank" rel="noopener noreferrer" className="inline-flex min-h-control items-center rounded-control bg-accent px-md text-body font-medium text-on-accent focus-visible:outline focus-visible:outline-2 focus-visible:outline-focus active:scale-[.98]">{t('web.detail.open_xianyu')}</a>}
            <CopyButton text={link ?? ''} disabled={!link} label={t('web.detail.copy_link')} copiedLabel={t('web.detail.copy_success')} errorLabel={t('web.detail.copy_failed')} />
            <Button busy={favorite.isPending} disabled={actionsBusy} onClick={() => favorite.mutate()}>{t(finding.listing.is_favorite ? 'web.detail.unfavorite' : 'web.detail.favorite')}</Button>
            <Button onClick={() => setSpecsOpen(true)} disabled={!profile || profile.specs.length === 0}>{t('web.detail.edit_specs')}</Button>
            <Button disabled={actionsBusy} busy={dismiss.isPending} onClick={() => { dismiss.reset(); if (finding.listing.is_dismissed) dismiss.mutate(false); else setConfirmAction('dismiss'); }}>{t(finding.listing.is_dismissed ? 'web.detail.restore' : 'web.detail.dismiss')}</Button>
            <Button disabled={!finding.listing.seller_id || actionsBusy} onClick={() => { block.reset(); setConfirmAction('block'); }}>{t('web.detail.block')}</Button>
            <Button busy={preview.isPending} onClick={() => { setFeedback(null); preview.mutate(); }}>{t('web.detail.reanalyze')}</Button>
            <Button busy={markRead.isPending} onClick={() => markRead.mutate(false)}>{t('web.detail.mark_unread')}</Button>
            <Button busy={refreshing} onClick={onRefresh}>{t('web.detail.refresh')}</Button>
          </div>
          {!finding.listing.seller_id && <p className="text-caption text-text-secondary">{t('web.detail.seller_id_missing')}</p>}
          <Deliveries finding={finding} {...(timeZone ? { timeZone } : {})} />
        </div>
      </div>
      {finding.listing.description && <details><summary className="min-h-control cursor-pointer py-md text-body font-medium">{t('web.detail.description')}</summary><p className="whitespace-pre-wrap break-words py-md text-body text-text-body">{finding.listing.description}</p></details>}
    </section>

    <Section title={t('web.detail.score_title')}><ScoreBreakdown finding={finding} /></Section>
    <Section title={t('web.detail.contact_title')}><ContactQuestions questions={finding.questions} /></Section>
    <Section title={t('web.detail.risks_title')}>
      <AnalysisWarnings finding={finding} linkChecks />
      {dictionary.isPending ? <Skeleton className="h-[8rem]" /> : dictionary.error ? <StatePanel kind="error" title={t('web.request_failed')} description={errorText(dictionary.error)} retry={{ label: t('common.retry'), run: () => { void dictionary.refetch(); } }} /> : <Risks finding={finding} dictionary={dictionary.data ?? []} />}
      <ExtraFindings finding={finding} />
    </Section>
    <Section title={t('web.detail.seller_title')}><Seller finding={finding} {...(timeZone ? { timeZone } : {})} /></Section>
    <Section title={t('web.detail.market_title')}><MarketSamples finding={finding} {...(timeZone ? { timeZone } : {})} /></Section>
    <Section title={t('web.detail.price_history')}><PriceHistory finding={finding} {...(timeZone ? { timeZone } : {})} /></Section>
    <Section title={t('web.detail.summary_title')}>
      <p className="whitespace-pre-wrap break-words text-body text-text-body">{finding.decision === 'scored' ? finding.summary ?? t('web.detail.no_analysis') : t('web.detail.no_analysis')}</p>
      {finding.decision === 'scored' && finding.analyzed_at !== null && <p className="text-caption text-text-secondary"><DateText value={finding.analyzed_at} {...(timeZone ? { timeZone } : {})} />{finding.analysis_model && ` · ${finding.analysis_model}`}</p>}
    </Section>
    {(finding.decision === 'pending' || finding.decision === 'failed' || reanalyze.data) && <Button busy={run.isPending} onClick={() => run.mutate()}>{t('web.detail.run_now')}</Button>}

    {specsOpen && <SpecsEditor finding={finding} open onOpenChange={setSpecsOpen} />}
    <ConfirmDialog open={confirmAction !== null} onOpenChange={(open) => { if (!open && !dismiss.isPending && !block.isPending) setConfirmAction(null); }} title={t(confirmAction === 'block' ? 'web.detail.block' : 'web.detail.dismiss')} description={t(confirmAction === 'block' ? 'web.detail.block_scope' : 'web.detail.dismiss_scope')} confirmLabel={t(confirmAction === 'block' ? 'web.detail.block' : 'web.detail.dismiss')} cancelLabel={t('common.cancel')} pending={dismiss.isPending || block.isPending} {...(confirmAction === 'block' && block.error ? { error: errorText(block.error) } : confirmAction === 'dismiss' && dismiss.error ? { error: errorText(dismiss.error) } : {})} onConfirm={() => { if (confirmAction === 'block') block.mutate(); else dismiss.mutate(true); }} destructive />
    <ConfirmDialog open={reanalysisOpen} onOpenChange={(open) => { if (!reanalyze.isPending) setReanalysisOpen(open); }} title={t('web.detail.reanalyze')} description={`${t('web.detail.reanalysis_cost')} ${preview.data ? t('web.detail.reanalyze_confirm', { count: preview.data.needs_detail_count }) : ''}`} confirmLabel={t('web.detail.reanalyze')} cancelLabel={t('common.cancel')} pending={reanalyze.isPending} {...(reanalyze.error ? { error: errorText(reanalyze.error) } : {})} onConfirm={() => reanalyze.mutate()} />
  </article>;
}

function Neighbor({ id, taskId, label, onNavigate }: { id: number | null; taskId: number; label: string; onNavigate?: (findingId: number, taskId: number) => void }) {
  if (id === null) return <Button disabled>{label}</Button>;
  if (onNavigate) return <Button onClick={() => onNavigate(id, taskId)}>{label}</Button>;
  return <Link to="/results" search={{ task_id: taskId, finding_id: id }} className="inline-flex min-h-control items-center rounded-control border border-border px-md text-body focus-visible:outline focus-visible:outline-2 focus-visible:outline-focus">{label}</Link>;
}

function MarketSamples({ finding, timeZone }: { finding: Finding; timeZone?: string }) {
  const { t, locale } = useTranslation();
  const message = useMessageText();
  const comparison = finding.market_comparison;
  const errorText = useApiError();
  const query = useMarketSamples(finding);
  const samples = query.data?.pages.flatMap((page) => page.items) ?? [];
  return <div className="space-y-lg">
    <p className="text-body text-text-secondary">{t('web.detail.asking_price_note')}</p>
    {comparison && <div className="space-y-sm"><h3 className="text-body font-medium">{t('web.detail.market_current')}</h3><p className="text-section"><Money value={comparison.fair.price} /></p><p className="text-body">{comparison.config_key === '*' ? t('web.detail.no_configuration') : comparison.config_key} · {t('web.detail.sample_count', { count: comparison.fair.sample })}</p><p className="text-body text-text-secondary">{message(comparison.fair.basis)}</p><p className="text-caption text-text-secondary">{t('web.detail.updated_at')} · <DateText value={comparison.as_of} {...(timeZone ? { timeZone } : {})} /></p></div>}
    {!comparison ? <p className="text-body text-text-secondary">{t('web.detail.market_empty')}</p> : query.isPending ? <StatePanel kind="pending" label={t('common.loading')} /> : null}
    {query.error && <StatePanel kind="error" title={t('web.request_failed')} description={errorText(query.error)} retry={{ label: t('common.retry'), run: () => { void query.refetch(); } }} />}
    {query.data && samples.length === 0 && <p className="text-body text-text-secondary">{t('web.detail.market_empty')}</p>}
    {samples.length > 0 && <div className="max-w-full overflow-x-auto rounded-control border border-border" role="region" aria-label={t('web.detail.market_title')} tabIndex={0}><table className="w-full text-left text-body"><thead><tr><th scope="col" className="p-md">{t('web.detail.sample_item')}</th><th scope="col" className="p-md text-right">{t('web.detail.price')}</th><th scope="col" className="p-md">{t('web.detail.observed_at')}</th></tr></thead><tbody>{samples.map((sample) => <tr key={sample.item_id} className="border-t border-border"><th scope="row" className="min-w-[10rem] p-md text-left font-normal">{safeExternalURL(sample.desktop_url) ? <a href={safeExternalURL(sample.desktop_url)} target="_blank" rel="noopener noreferrer" className="inline-flex min-h-control items-center break-words text-accent-text underline underline-offset-4">{sample.title}</a> : sample.title}</th><td className="whitespace-nowrap p-md text-right"><Money value={sample.price} /></td><td className="min-w-[10rem] p-md"><DateText value={sample.observed_at} {...(timeZone ? { timeZone } : {})} /></td></tr>)}</tbody></table></div>}
    {query.hasNextPage && <Button busy={query.isFetchingNextPage} onClick={() => { void query.fetchNextPage(); }}>{t('web.detail.load_more')}</Button>}
    <div className="space-y-md"><h3 className="text-body font-medium">{t('web.detail.adjacent_title')}</h3>{comparison?.adjacent_prices.length ? comparison.adjacent_prices.map((adjacent) => <p key={`${adjacent.spec_name}:${adjacent.from}`} className="break-words text-body">{t('web.detail.adjacent_price', { from: `${adjacent.spec_name} ${adjacent.from}`, to: adjacent.to, premium: formatMoney(adjacent.item_premium, locale), typical: formatMoney(adjacent.typical_premium, locale) })}</p>) : <p className="text-body text-text-secondary">{t('web.detail.adjacent_empty')}</p>}</div>
  </div>;
}
