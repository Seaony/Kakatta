import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { apiData, useApi, useApiError } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import type { Schema } from '../../api/types';
import { Button, CopyButton, InlineMessage, StatePanel, StatusBadge } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { formatMoney, formatSellerName } from '../../i18n/format';
import { ItemImage } from './finding-card';
import { resultTime, safeExternalURL, type SavedListing } from './model';

function useListingActions(itemId: string, onChanged?: (listing: SavedListing) => void) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (patch: Schema<'ListingPatch'>) => apiData(await api.PATCH('/api/v1/listings/{item_id}', {
      params: { path: { item_id: itemId } }, body: patch,
    })),
    onSuccess: async (listing) => {
      await Promise.all(['listings', 'findings', 'tasks', 'overview'].map((resource) => queryClient.invalidateQueries({ queryKey: apiKey(resource) })));
      onChanged?.(listing);
    },
  });
}

export function SavedListingCard({ listing, onOpen, onChanged }: { listing: SavedListing; onOpen(): void; onChanged?(listing: SavedListing): void }) {
  const { t, locale } = useTranslation();
  const errorText = useApiError();
  const mutation = useListingActions(listing.item_id, onChanged);
  return (
    <article className="grid min-w-0 grid-cols-[4.5rem_minmax(0,1fr)] gap-md border-b border-border py-xl sm:grid-cols-[7.5rem_minmax(0,1fr)] sm:gap-lg">
      <div><ItemImage listing={listing} /></div>
      <div className="min-w-0 space-y-md">
        <Button variant="plain" className="justify-start px-0 text-left text-section font-semibold" onClick={onOpen}>{listing.title || t('results.card.untitled')}</Button>
        <p className="text-section font-semibold tabular-nums">{listing.price === null ? t('results.price_unknown') : formatMoney(listing.price, locale)}</p>
        <div className="flex flex-wrap gap-sm">
          {listing.sale_status === 'sold' && <StatusBadge>{t('results.card.sold')}</StatusBadge>}
          {listing.sale_status === 'offline' && <StatusBadge>{t('results.card.offline')}</StatusBadge>}
          {listing.last_drop_amount !== null && listing.last_drop_amount > 0 && <StatusBadge tone="positive">{t('results.card.drop', { amount: formatMoney(listing.last_drop_amount, locale) })}</StatusBadge>}
        </div>
        <p className="text-body text-text-secondary">{formatSellerName(listing.seller?.nick, locale, listing.seller_nick)} {listing.area}</p>
        {listing.findings.length === 0 && <p className="text-body text-text-secondary">{t(listing.is_favorite ? 'results.card.saved_without_task' : 'results.listing.no_findings')}</p>}
        {mutation.error && <InlineMessage tone="danger">{errorText(mutation.error)}</InlineMessage>}
        <div className="flex flex-wrap gap-sm">
          <Button busy={mutation.isPending} onClick={() => mutation.mutate({ is_favorite: !listing.is_favorite })}>{t(listing.is_favorite ? 'results.action.unfavorite' : 'results.action.favorite')}</Button>
          <Button busy={mutation.isPending} onClick={() => mutation.mutate({ is_dismissed: !listing.is_dismissed })}>{t(listing.is_dismissed ? 'results.action.restore' : 'results.action.dismiss')}</Button>
          <Button onClick={onOpen}>{t('results.action.detail')}</Button>
        </div>
      </div>
    </article>
  );
}

export function ListingPanel({ itemId }: { itemId: string }) {
  const api = useApi();
  const { t, locale } = useTranslation();
  const errorText = useApiError();
  const mutation = useListingActions(itemId);
  const item = useQuery({
    queryKey: apiKey('listings', itemId),
    queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/listings/{item_id}', { params: { path: { item_id: itemId } }, signal })),
  });
  const settings = useQuery({ queryKey: apiKey('settings'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings', { signal })) });
  const configuredZone = settings.data?.values['general.timezone'];
  const timeZone = typeof configuredZone === 'string' ? configuredZone : undefined;
  const prices = useInfiniteQuery({
    queryKey: apiKey('listings', itemId, 'prices'), initialPageParam: '',
    queryFn: async ({ pageParam, signal }) => apiData(await api.GET('/api/v1/listings/{item_id}/prices', {
      params: { path: { item_id: itemId }, query: { limit: 50, ...(pageParam ? { cursor: pageParam } : {}) } }, signal,
    })),
    getNextPageParam: (page) => page.next_cursor ?? undefined,
  });
  if (!item.data) return item.error
    ? <StatePanel kind="error" title={t('web.request_failed')} description={errorText(item.error)} retry={{ label: t('common.retry'), run: () => { void item.refetch(); } }} />
    : <StatePanel kind="pending" label={t('results.loading')} />;
  const listing = item.data;
  const link = safeExternalURL(listing.desktop_url);
  return (
    <section className="min-w-0 space-y-xl" aria-label={t('results.listing.title')}>
      {item.error && <InlineMessage tone="warning" action={<Button onClick={() => { void item.refetch(); }}>{t('common.retry')}</Button>}>{t('results.refresh_failed')} {errorText(item.error)}</InlineMessage>}
      <div className="grid min-w-0 gap-lg sm:grid-cols-[7.5rem_minmax(0,1fr)]">
        <div className="max-w-[12rem]"><ItemImage listing={listing} /></div>
        <div className="min-w-0 space-y-md">
          <h2 className="break-words text-section font-semibold">{listing.title || t('results.card.untitled')}</h2>
          <p className="text-section font-semibold tabular-nums">{listing.price === null ? t('results.price_unknown') : formatMoney(listing.price, locale)}</p>
          <p className="break-words text-body text-text-label">{formatSellerName(listing.seller?.nick, locale, listing.seller_nick)} {listing.area}</p>
          <p className="text-body text-text-secondary">{t('results.card.published', { time: listing.published_at === null ? t('results.published_unknown') : resultTime(listing.published_at, locale, timeZone) })}</p>
          <div className="flex flex-wrap gap-sm">
            <Button busy={mutation.isPending} onClick={() => mutation.mutate({ is_favorite: !listing.is_favorite })}>{t(listing.is_favorite ? 'results.action.unfavorite' : 'results.action.favorite')}</Button>
            <Button busy={mutation.isPending} onClick={() => mutation.mutate({ is_dismissed: !listing.is_dismissed })}>{t(listing.is_dismissed ? 'results.action.restore' : 'results.action.dismiss')}</Button>
            <CopyButton text={link ?? ''} disabled={!link} label={t('results.action.copy')} copiedLabel={t('results.action.copied')} errorLabel={t('results.action.copy_failed')} />
            {link && <a href={link} target="_blank" rel="noopener noreferrer" className="inline-flex min-h-control items-center rounded-control px-md text-body text-accent-text underline underline-offset-4 focus-visible:outline focus-visible:outline-2 focus-visible:outline-focus">{t('results.action.open')}</a>}
          </div>
          {mutation.error && <InlineMessage tone="danger">{errorText(mutation.error)}</InlineMessage>}
        </div>
      </div>
      <div className="space-y-md">
        <h3 className="text-section font-semibold">{t('results.listing.description')}</h3>
        <p className="whitespace-pre-wrap break-words text-body text-text-body">{listing.description ?? t('results.listing.no_description')}</p>
      </div>
      <div className="space-y-md">
        <h3 className="text-section font-semibold">{t('results.listing.findings')}</h3>
        {listing.findings.length === 0 ? <p className="text-body text-text-secondary">{t('results.listing.no_findings')}</p>
          : <ul className="space-y-sm">{listing.findings.map((finding) => <li key={finding.id}>
            <Link to="/results" search={{ task_id: finding.task_id, finding_id: finding.id }} className="inline-flex min-h-control items-center text-body text-accent-text underline underline-offset-4 focus-visible:outline focus-visible:outline-2 focus-visible:outline-focus">{finding.task.name}</Link>
          </li>)}</ul>}
      </div>
      <div className="space-y-md">
        <h3 className="text-section font-semibold">{t('results.listing.price_history')}</h3>
        {prices.isPending && <StatePanel kind="pending" label={t('common.loading')} />}
        {prices.error && <InlineMessage tone="danger" action={<Button onClick={() => { void prices.refetch(); }}>{t('common.retry')}</Button>}>{errorText(prices.error)}</InlineMessage>}
        {prices.data && prices.data.pages[0]?.total === 0 ? <p className="text-body text-text-secondary">{t('results.listing.no_prices')}</p> : <ul className="space-y-sm">
          {prices.data?.pages.flatMap((page) => page.items).map((price) => <li key={price.id} className="flex flex-wrap gap-x-lg gap-y-sm text-body tabular-nums"><time dateTime={new Date(price.observed_at).toISOString()}>{resultTime(price.observed_at, locale, timeZone)}</time><span>{formatMoney(price.price, locale)}</span></li>)}
        </ul>}
        {prices.hasNextPage && <Button busy={prices.isFetchingNextPage} onClick={() => { void prices.fetchNextPage(); }}>{t('results.listing.more_prices')}</Button>}
      </div>
    </section>
  );
}
