import { useQuery } from '@tanstack/react-query';
import { apiData, useApi } from '../../api/hooks';
import { ApiError } from '../../api/errors';
import { apiKey } from '../../api/keys';
import type { Schema } from '../../api/types';

export function useTaskAccounts() {
  const api = useApi();
  return useQuery({ queryKey: apiKey('accounts', 'task-options'), queryFn: async ({ signal }) => {
    const items: Schema<'AccountView'>[] = [];
    const seen = new Set<string>();
    let cursor: string | undefined;
    do {
      const result = apiData(await api.GET('/api/v1/accounts', { params: { query: { limit: 200, ...(cursor ? { cursor } : {}) } }, signal }));
      items.push(...result.items);
      cursor = result.next_cursor ?? undefined;
      if (cursor && seen.has(cursor)) throw new ApiError(0, { code: 'web.invalid_response', args: {} });
      if (cursor) seen.add(cursor);
    } while (cursor);
    return items;
  } });
}
