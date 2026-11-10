import { createRouter, type RouterHistory } from '@tanstack/react-router';
import type { QueryClient } from '@tanstack/react-query';
import type { ApiClient } from '../api/client';
import { routeTree } from '../routeTree.gen';

export type RouterContext = { queryClient: QueryClient; api: ApiClient };

export function createAppRouter(context: RouterContext, history?: RouterHistory) {
  return createRouter({ routeTree, context, defaultPreload: false, scrollRestoration: true, ...(history ? { history } : {}) });
}

export type AppRouter = ReturnType<typeof createAppRouter>;

declare module '@tanstack/react-router' {
  interface Register { router: AppRouter }
}
