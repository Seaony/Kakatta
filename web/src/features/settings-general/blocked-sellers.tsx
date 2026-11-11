import { useState } from 'react';
import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiData, useApi, useApiError } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import { ApiError, isCancellation } from '../../api/errors';
import { useTranslation } from '../../i18n';
import { formatDateTime, formatSellerName } from '../../i18n/format';
import { Button } from '../../components/ui/button';
import { StatePanel } from '../../components/ui/state-panel';
import { FormSection, InlineMessage, Pagination } from '../../components/ui';
import { useCancellableAction } from './use-cancellable-action';

export function BlockedSellers({ timeZone, disabled = false }: { timeZone?: string; disabled?: boolean }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const { t, locale } = useTranslation();
  const errorText = useApiError();
  const [cursors, setCursors] = useState<(string | undefined)[]>([undefined]);
  const [page, setPage] = useState(1);
  const cursor = cursors[page - 1];
  const [pendingSeller, setPendingSeller] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [uncertain, setUncertain] = useState(false);
  const [done, setDone] = useState(false);
  const action = useCancellableAction(() => { setPendingSeller(null); setUncertain(true); });
  const query = useQuery({ queryKey: apiKey('blocked-sellers', { cursor }), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/blocked-sellers', { params: { query: { limit: 50, ...(cursor ? { cursor } : {}) } }, signal })), placeholderData: keepPreviousData, enabled: action.enabled });

  async function unblock(sellerID: string) {
    if (disabled || pendingSeller !== null || !action.isActive()) return;
    const abort = action.start();
    if (!abort) return;
    setPendingSeller(sellerID); setError(null); setDone(false); setUncertain(false);
    try {
      await api.DELETE('/api/v1/blocked-sellers/{seller_id}', { params: { path: { seller_id: sellerID } }, signal: abort.signal });
      if (!action.isCurrent(abort)) return;
      setDone(true);
      if (query.data?.items.length === 1 && page > 1) setPage(page - 1);
      void queryClient.invalidateQueries({ queryKey: apiKey('blocked-sellers') });
      void queryClient.invalidateQueries({ queryKey: apiKey('findings') });
      void queryClient.invalidateQueries({ queryKey: apiKey('listings') });
    } catch (failure) {
      if (action.isCurrent(abort)) {
        if (isCancellation(failure) || abort.signal.aborted) setUncertain(true);
        else setError(failure);
      }
    } finally {
      if (action.finish(abort)) setPendingSeller(null);
    }
  }

  function date(value: number | null) {
    if (value === null || !timeZone) return t('web.settings.unknown_time');
    try { return formatDateTime(value, locale, timeZone); } catch { return t('web.settings.unknown_time'); }
  }

  return <FormSection title={t('web.settings.blocked_title')} description={t('web.settings.blocked_description')} actions={<Button busy={query.isFetching} disabled={!action.enabled} onClick={() => { void query.refetch(); }}>{t('web.settings.refresh')}</Button>}>
    {error !== null && <InlineMessage tone="danger"><p>{errorText(error)}</p>{error instanceof ApiError && error.requestID && <p className="mt-sm break-all text-caption">{t('web.request_id', { request_id: error.requestID })}</p>}</InlineMessage>}
    {uncertain && <InlineMessage tone="warning">{t('web.settings.unblock_uncertain')}</InlineMessage>}
    {done && <InlineMessage tone="positive">{t('web.settings.unblocked')}</InlineMessage>}
    {query.error && <InlineMessage tone="danger" action={<Button onClick={() => { void query.refetch(); }}>{t('common.retry')}</Button>}><p>{errorText(query.error)}</p>{query.error instanceof ApiError && query.error.requestID && <p className="mt-sm break-all text-caption">{t('web.request_id', { request_id: query.error.requestID })}</p>}</InlineMessage>}
    {!query.data ? (!query.error && <StatePanel kind="pending" label={t('common.loading')} />) : <>
      <p className="text-body tabular-nums text-text-secondary">{t('web.settings.blocked_count', { count: query.data.total })}</p>
      {query.data.items.length === 0
        ? <StatePanel kind="empty" title={t(page === 1 ? 'web.settings.blocked_empty' : 'web.settings.blocked_page_empty')} description={t('web.settings.blocked_empty_description')} />
        : <ul className="divide-y divide-border">{query.data.items.map((seller) => <li key={seller.seller_id} className="flex min-w-0 flex-wrap items-center justify-between gap-lg py-lg">
          <div className="min-w-0 space-y-xs"><p className="break-words text-body font-medium">{formatSellerName(seller.nick, locale)}</p><p className="break-all text-body text-text-secondary">{seller.seller_id}</p><p className="text-caption tabular-nums text-text-secondary">{date(seller.created_at)}</p></div>
          <Button busy={pendingSeller === seller.seller_id} disabled={disabled || !action.enabled || pendingSeller !== null || query.isPlaceholderData} onClick={() => { void unblock(seller.seller_id); }} aria-label={t('web.settings.unblock_named', { name: seller.nick?.trim() ? seller.nick : seller.seller_id })}>{t('web.settings.unblock')}</Button>
        </li>)}</ul>}
      {pendingSeller !== null && <Button onClick={action.cancel}>{t('web.settings.stop_waiting')}</Button>}
      <Pagination page={page} hasNextPage={query.data.next_cursor !== null} disabled={!action.enabled || query.isFetching || pendingSeller !== null} previousLabel={t('web.settings.previous_page')} nextLabel={t('web.settings.next_page')} pageLabel={t('web.settings.page_number', { page })} onPageChange={(next) => {
        if (next < page) { setPage(next); return; }
        const nextCursor = query.data?.next_cursor;
        if (nextCursor) { setCursors((current) => [...current.slice(0, page), nextCursor]); setPage(next); }
      }} />
    </>}
  </FormSection>;
}
