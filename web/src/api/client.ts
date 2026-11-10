import createClient from 'openapi-fetch';
import type { paths } from './schema.gen';
import { ApiError, isCancellation, responseError } from './errors';

export type ApiFetch = (request: Request) => Promise<Response>;

export function createApiClient(options: { fetch?: ApiFetch } = {}) {
  const transport = options.fetch ?? globalThis.fetch.bind(globalThis);
  const authenticationListeners = new Set<() => void>();
  const authenticationVersions = new WeakMap<Request, number>();
  let authenticationVersion = 0;
  let authenticationLost = false;
  const client = createClient<paths>({
    baseUrl: globalThis.location.origin,
    credentials: 'same-origin',
    fetch: async (request) => {
      const path = new URL(request.url).pathname;
      if (authenticationLost && path !== '/healthz' && path !== '/api/v1/setup' && path !== '/api/v1/me' && path !== '/api/v1/auth/login' && path !== '/api/v1/auth/totp') {
        throw new ApiError(401, { code: 'auth.unauthenticated', args: {} });
      }
      try {
        return await transport(request);
      } catch (error) {
        if (request.signal.aborted || isCancellation(error)) throw error;
        throw new ApiError(0, { code: 'web.network_failed', args: {} });
      }
    },
  });
  client.use({
    onRequest({ request }) { authenticationVersions.set(request, authenticationVersion); },
    async onResponse({ request, response }) {
      if (!response.ok) {
        const error = await responseError(response);
        if (error.status === 401 && (error.reason.code === 'auth.unauthenticated' || new URL(request.url).pathname === '/api/v1/me') && !request.signal.aborted && authenticationVersions.get(request) === authenticationVersion) {
          authenticationLost = true;
          for (const listener of authenticationListeners) listener();
        }
        throw error;
      }
    },
  });
  // 成功响应的解析失败也在这里收口，不重复读取响应正文。
  for (const method of ['request', 'GET', 'PUT', 'POST', 'DELETE', 'OPTIONS', 'HEAD', 'PATCH', 'TRACE'] as const) {
    const request = client[method];
    Object.defineProperty(client, method, {
      value: (...args: unknown[]) => Reflect.apply(request, client, args).catch((error: unknown) => {
        if (error instanceof SyntaxError) throw new ApiError(0, { code: 'web.invalid_response', args: {} });
        throw error;
      }),
    });
  }
  return Object.assign(client, {
    subscribeAuthenticationLost(listener: () => void) {
      authenticationListeners.add(listener);
      return () => { authenticationListeners.delete(listener); };
    },
    restoreAuthentication() { authenticationVersion++; authenticationLost = false; },
  });
}

export type ApiClient = ReturnType<typeof createApiClient>;
