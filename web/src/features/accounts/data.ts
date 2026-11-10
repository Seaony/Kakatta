import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query';
import { useRef, useState } from 'react';
import { ApiError } from '../../api/errors';
import { apiData, useApi, useApiError, useMessageText } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import type { Schema } from '../../api/types';
import { useAuthentication } from '../../app/auth-state';

export type Account = Schema<'AccountView'>;
export type Proxy = Schema<'ProxyView'>;
export type Session = Schema<'SessionView'>;

export function useAccounts() {
  const api = useApi();
  const auth = useAuthentication();
  return useInfiniteQuery({
    enabled: auth?.authenticated !== false,
    queryKey: apiKey('accounts', 'list'),
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam, signal }) => apiData(await api.GET('/api/v1/accounts', {
      params: { query: { limit: 50, ...(pageParam ? { cursor: pageParam } : {}) } }, signal,
    })),
    getNextPageParam: (page) => page.next_cursor ?? undefined,
  });
}

export function useProxies() {
  const api = useApi();
  const auth = useAuthentication();
  return useInfiniteQuery({
    enabled: auth?.authenticated !== false,
    queryKey: apiKey('proxies', 'list'),
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam, signal }) => apiData(await api.GET('/api/v1/proxies', {
      params: { query: { limit: 50, ...(pageParam ? { cursor: pageParam } : {}) } }, signal,
    })),
    getNextPageParam: (page) => page.next_cursor ?? undefined,
  });
}

export function useAccount(id: number) {
  const api = useApi();
  const auth = useAuthentication();
  return useQuery({
    enabled: auth?.authenticated !== false,
    queryKey: apiKey('accounts', 'detail', id),
    queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/accounts/{account_id}', {
      params: { path: { account_id: id } }, signal,
    })),
    refetchInterval: (query) => auth?.authenticated !== false && !query.state.error && query.state.data?.session ? 2_000 : false,
  });
}

export function useAccountCache() {
  const queryClient = useQueryClient();
  return {
    save(account: Account) {
      queryClient.setQueryData(apiKey('accounts', 'detail', account.id), account);
    },
    changed() {
      void queryClient.invalidateQueries({ queryKey: apiKey('accounts') });
      void queryClient.invalidateQueries({ queryKey: apiKey('tasks') });
      void queryClient.invalidateQueries({ queryKey: apiKey('system-status') });
    },
  };
}

// 写请求在组件里保存状态，代理密码不会进入 mutation 缓存。
export function useOperation() {
  const lock = useRef(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const describe = useApiError();
  const message = useMessageText();
  const auth = useAuthentication();
  const nextAllowed = error instanceof ApiError ? error.reason.args.next_allowed_at : undefined;
  return {
    pending,
    error,
    nextAllowedAt: typeof nextAllowed === 'number' && Number.isSafeInteger(nextAllowed) ? nextAllowed : null,
    errorText: error ? `${describe(error)}${error instanceof ApiError && error.requestID ? ` (${error.requestID})` : ''}` : undefined,
    clearError() { setError(null); },
    fieldError(key: string) {
      const reason = error instanceof ApiError ? error.fields.find((field) => field.key === key)?.reason : undefined;
      return reason ? message(reason) : undefined;
    },
    async run<T>(action: () => Promise<T>, success: (result: T) => void | Promise<void>) {
      if (lock.current || auth?.authenticated === false) return;
      lock.current = true;
      setPending(true);
      setError(null);
      try { await success(await action()); }
      catch (failure) { setError(failure); }
      finally { lock.current = false; setPending(false); }
    },
  };
}

export function uniqueByID<T extends { id: number }>(items: readonly T[]): T[] {
  return [...new Map(items.map((item) => [item.id, item])).values()];
}
