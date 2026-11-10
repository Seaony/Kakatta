import { useInfiniteQuery, useQueryClient } from '@tanstack/react-query';
import type { components } from '../../api/schema.gen';
import { useApi, apiData, useApiError } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import { ApiError } from '../../api/errors';
import { Button, Dialog, StatePanel, Skeleton, InlineMessage } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { formatDateTime, formatMoney } from '../../i18n/format';
import { safeListingURL } from './format';

type MarketGroup = components['schemas']['AnalysisMarketGroup'];

export function MarketSamples({ taskId, group, config, timezone, onClose }: {
  taskId: number; group: MarketGroup; config: string; timezone: string | undefined; onClose(): void;
}) {
  const api = useApi();
  const queryClient = useQueryClient();
  const errorText = useApiError();
  const { t, locale } = useTranslation();
  const key = apiKey('tasks', taskId, 'market', 'samples', group.config_key, group.brand_new);
  const query = useInfiniteQuery({
    queryKey: key,
    initialPageParam: '',
    queryFn: async ({ signal, pageParam }) => apiData(await api.GET('/api/v1/tasks/{task_id}/market/samples', {
      signal,
      params: { path: { task_id: taskId }, query: {
        config_key: group.config_key, brand_new: group.brand_new, limit: 50,
        ...(pageParam ? { cursor: pageParam } : {}),
      } },
    })),
    getNextPageParam: (lastPage) => lastPage.next_cursor ?? undefined,
  });
  const items = query.data?.pages.flatMap((page) => page.items) ?? [];
  const total = query.data?.pages.at(-1)?.total ?? 0;
  const retry = () => {
    if (query.error instanceof ApiError && query.error.status === 400) void queryClient.resetQueries({ queryKey: key, exact: true });
    else if (query.isFetchNextPageError) void query.fetchNextPage();
    else void query.refetch();
  };

  return <Dialog open onOpenChange={(open) => { if (!open) onClose(); }} title={t('web.market.samples_title', { config })} description={t('web.market.samples_help')} closeLabel={t('common.close')}>
    <div className="space-y-lg">
      {query.isPending && <StatePanel kind="pending" label={t('common.loading')}><Skeleton /><Skeleton /></StatePanel>}
      {query.isError && <InlineMessage tone="danger" action={<Button onClick={retry}>{t('common.retry')}</Button>}>
        <p>{errorText(query.error)}</p>
        {query.error instanceof ApiError && query.error.requestID && <p>{t('web.request_id', { request_id: query.error.requestID })}</p>}
      </InlineMessage>}
      {query.data && items.length === 0 && <StatePanel kind="empty" title={t('web.market.samples_empty')} />}
      {items.length > 0 && <>
        <p className="text-caption tabular-nums text-text-secondary">{t('web.market.samples_progress', { shown: items.length, total, count: total })}</p>
        <ol className="divide-y divide-border">
          {items.map((item) => <li key={item.item_id} className="min-w-0 space-y-sm py-lg">
            <div className="flex flex-wrap items-baseline justify-between gap-md">
              <p className="min-w-0 break-words font-medium">{item.title || t('web.market.sample_listing')}</p>
              <span className="shrink-0 tabular-nums">{formatMoney(item.price, locale)}</span>
            </div>
            <p className="break-words text-caption text-text-secondary">{item.config_key === '*' ? t('web.market.no_configuration') : item.config_key} · {item.brand_new ? t('web.market.new') : t('web.market.used')}</p>
            <p className="text-caption text-text-secondary">{t('web.market.observed_at')}: {timezone ? formatDateTime(item.observed_at, locale, timezone) : t('web.market.timezone_unavailable')}</p>
            {safeListingURL(item.desktop_url) && <a href={safeListingURL(item.desktop_url)} target="_blank" rel="noopener noreferrer" className="inline-flex min-h-control items-center rounded-control text-body text-accent-text underline underline-offset-4">{t('web.market.open_listing')}</a>}
          </li>)}
        </ol>
        {query.hasNextPage && <Button busy={query.isFetchingNextPage} busyLabel={t('common.loading')} onClick={() => { void query.fetchNextPage(); }}>{t('web.market.load_more')}</Button>}
      </>}
    </div>
  </Dialog>;
}
