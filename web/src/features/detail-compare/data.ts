import { useInfiniteQuery, useQuery, type QueryClient } from '@tanstack/react-query';
import { apiData, useApi } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import type { RequestQuery, Schema } from '../../api/types';

export type FindingDetailProps = {
  findingId: number;
  query?: RequestQuery<'getFinding'>;
  onClose?: () => void;
  onNavigate?: (findingId: number, taskId: number) => void;
  timeZone?: string;
};

export type ComparePageProps = { ids: readonly number[]; timeZone?: string };
export type Finding = Schema<'FindingView'>;

export function useFinding(findingId: number, query?: RequestQuery<'getFinding'>, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: apiKey('findings', 'detail', findingId, query ?? {}),
    enabled: enabled && Number.isSafeInteger(findingId) && findingId > 0,
    queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/findings/{finding_id}', {
      params: { path: { finding_id: findingId }, ...(query ? { query } : {}) }, signal,
    })),
  });
}

export function usePriceHistory(itemId: string) {
  const api = useApi();
  return useInfiniteQuery({
    queryKey: apiKey('listings', itemId, 'prices'),
    initialPageParam: '',
    queryFn: async ({ signal, pageParam }) => apiData(await api.GET('/api/v1/listings/{item_id}/prices', {
      params: { path: { item_id: itemId }, query: { limit: 50, ...(pageParam ? { cursor: pageParam } : {}) } }, signal,
    })),
    getNextPageParam: (page) => page.next_cursor ?? undefined,
  });
}

export function useMarketSamples(finding: Finding) {
  const api = useApi();
  const comparison = finding.market_comparison;
  const config = comparison?.config_key;
  return useInfiniteQuery({
    queryKey: apiKey('tasks', finding.task_id, 'market-samples', config, comparison?.brand_new),
    enabled: config !== null && config !== undefined,
    initialPageParam: '',
    queryFn: async ({ signal, pageParam }) => apiData(await api.GET('/api/v1/tasks/{task_id}/market/samples', {
      params: {
        path: { task_id: finding.task_id },
        query: { config_key: config ?? '', brand_new: comparison?.brand_new === true, limit: 50, ...(pageParam ? { cursor: pageParam } : {}) },
      }, signal,
    })),
    getNextPageParam: (page) => page.next_cursor ?? undefined,
  });
}

export async function invalidateResults(client: QueryClient) {
  await Promise.all(['findings', 'listings', 'tasks', 'overview', 'blocked-sellers'].map((resource) =>
    client.invalidateQueries({ queryKey: apiKey(resource) }),
  ));
}

function hasProfileFields(profile: Schema<'AnalysisProfile'>): profile is Schema<'AnalysisProfileFields'> {
  return Array.isArray(profile.specs);
}

export function profileFields(profile: Schema<'AnalysisProfile'>): Schema<'AnalysisProfileFields'> | null {
  return hasProfileFields(profile) ? profile : null;
}

export function safeExternalURL(value: string | null): string | undefined {
  if (!value) return undefined;
  try {
    const url = new URL(value);
    return url.protocol === 'https:' || url.protocol === 'http:' ? url.href : undefined;
  } catch { return undefined; }
}
