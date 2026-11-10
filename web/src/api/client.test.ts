import { expect, test } from 'bun:test';
import { createApiClient, type ApiFetch } from './client';
import { ApiError } from './errors';

function fetchStub(handle: (request: Request) => Promise<Response> | Response): ApiFetch {
  return async (request) => handle(request);
}

test('同源路径与 Cookie 策略经过统一 client，不启动外部请求', async () => {
  let received: Request | undefined;
  const api = createApiClient({ fetch: fetchStub((request) => { received = request; return Response.json({}); }) });
  await api.GET('/api/v1/me');
  expect(received?.url).toBe('http://localhost/api/v1/me');
  expect(received?.credentials).toBe('same-origin');
  expect(received?.method).toBe('GET');
});

test('401 与 409 统一保留原因、字段错误和请求编号，不依赖中文正文', async () => {
  for (const status of [401, 409]) {
    const api = createApiClient({ fetch: fetchStub(() => Response.json({
      error: { code: 'auth.invalid_credentials', args: {} },
      fields: [{ key: 'password', reason: { code: 'auth.invalid_credentials', args: {} } }],
      request_id: 'test-request',
    }, { status })) });
    try {
      await api.GET('/api/v1/me');
      throw new Error('没有拦住失败响应');
    } catch (error) {
      expect(error).toBeInstanceOf(ApiError);
      const failure = error as ApiError;
      expect(failure.status).toBe(status);
      expect(failure.reason).toEqual({ code: 'auth.invalid_credentials', args: {} });
      expect(failure.fields[0]?.key).toBe('password');
      expect(failure.requestID).toBe('test-request');
    }
  }
});

test('异常响应不回显正文，成功响应的坏 JSON 也转成统一错误', async () => {
  for (const response of [
    new Response('private-upstream-body', { status: 503 }),
    Response.json({ error: { code: 'broken', args: null }, fields: [], request_id: 'test' }, { status: 400 }),
    new Response('{"private":"incomplete', { headers: { 'Content-Type': 'application/json' } }),
  ]) {
    const api = createApiClient({ fetch: fetchStub(() => response) });
    await expect(api.GET('/api/v1/me')).rejects.toMatchObject({ name: 'ApiError', message: 'web.invalid_response' });
  }
});

test('取消贯穿 Request.signal，保留取消原因，不变成网络故障', async () => {
  const abort = new AbortController();
  let seen: AbortSignal | undefined;
  const api = createApiClient({ fetch: fetchStub((request) => {
    seen = request.signal;
    return new Promise((_resolve, reject) => {
      if (request.signal.aborted) reject(request.signal.reason);
      else request.signal.addEventListener('abort', () => reject(request.signal.reason), { once: true });
    });
  }) });
  const pending = api.GET('/api/v1/me', { signal: abort.signal });
  abort.abort();
  await expect(pending).rejects.toMatchObject({ name: 'AbortError' });
  expect(seen?.aborted).toBe(true);
});

test('网络失败使用固定原因，不保留 transport 的地址或凭据', async () => {
  const api = createApiClient({ fetch: fetchStub(() => { throw new TypeError('private-address-token'); }) });
  await expect(api.GET('/api/v1/me')).rejects.toMatchObject({ status: 0, reason: { code: 'web.network_failed', args: {} } });
});
