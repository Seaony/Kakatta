import createClient from 'openapi-fetch';
import type { paths } from './schema.gen';
import { ApiError, isCancellation, responseError } from './errors';

export type ApiFetch = (request: Request) => Promise<Response>;

export function createApiClient(options: { fetch?: ApiFetch } = {}) {
  const transport = options.fetch ?? globalThis.fetch.bind(globalThis);
  const client = createClient<paths>({
    baseUrl: globalThis.location.origin,
    credentials: 'same-origin',
    fetch: async (request) => {
      try {
        return await transport(request);
      } catch (error) {
        if (request.signal.aborted || isCancellation(error)) throw error;
        throw new ApiError(0, { code: 'web.network_failed', args: {} });
      }
    },
  });
  client.use({
    async onResponse({ response }) {
      if (!response.ok) throw await responseError(response);
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
  return client;
}

export type ApiClient = ReturnType<typeof createApiClient>;
