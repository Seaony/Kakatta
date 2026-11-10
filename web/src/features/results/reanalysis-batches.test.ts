import { describe, expect, test } from 'bun:test';
import { createApiClient } from '../../api/client';
import { ApiError } from '../../api/errors';
import type { Schema } from '../../api/types';
import { previewReanalysisBatches, queueReanalysisBatches, type ReanalysisProgress } from './reanalysis-batches';

const ids = (count: number) => Array.from({ length: count }, (_, index) => index + 1);
const json = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } });
function result(selected: readonly number[]): Schema<'ReanalysisResult'> {
  return { items: selected.map((finding_id) => ({ finding_id, status: 'queued', reason: null, needs_detail: true })), needs_detail_count: selected.length };
}

describe('共享重新分析批次', () => {
  test('预检稳定去重且每批最多200，数字来自原响应，as_of取最后一批', async () => {
    const selected = Object.freeze([3, 1, 2, ...ids(415).slice(3), 3, 201]);
    const requests: number[][] = [];
    const responses: Schema<'ReanalysisPreview'>[] = [
      { as_of: 100, requested_count: 200, needs_detail_count: 7, cached_detail_count: 193 },
      { as_of: 50, requested_count: 200, needs_detail_count: 9, cached_detail_count: 191 },
      { as_of: 75, requested_count: 15, needs_detail_count: 2, cached_detail_count: 13 },
    ];
    const api = createApiClient({ fetch: async (request) => {
      expect(new URL(request.url).pathname).toBe('/api/v1/findings/reanalysis-preview');
      const body: Schema<'FindingSelection'> = await request.json();
      requests.push(body.ids);
      return json(responses[requests.length - 1]);
    } });
    expect(await previewReanalysisBatches(api, selected, new AbortController().signal)).toEqual({ as_of: 75, requested_count: 415, needs_detail_count: 18, cached_detail_count: 397 });
    expect(requests.map((batch) => batch.length)).toEqual([200, 200, 15]);
    expect(requests.flat()).toEqual([...new Set(selected)]);
  });

  test('预检取消后忽略迟到响应，不读取下一批', async () => {
    let start: (() => void) | undefined;
    const started = new Promise<void>((resolve) => { start = resolve; });
    let finish: ((response: Response) => void) | undefined;
    let calls = 0;
    const controller = new AbortController();
    const api = createApiClient({ fetch: async () => {
      calls++;
      return await new Promise<Response>((resolve) => { finish = resolve; start?.(); });
    } });
    const pending = previewReanalysisBatches(api, ids(215), controller.signal);
    await started;
    const reason = new DOMException('取消预检', 'AbortError');
    const rejected = pending.then(() => undefined, (value: unknown) => value);
    controller.abort(reason);
    finish?.(json({ as_of: 1, requested_count: 200, needs_detail_count: 1, cached_detail_count: 199 } satisfies Schema<'ReanalysisPreview'>));
    expect(await rejected).toBe(reason);
    expect(calls).toBe(1);
  });

  test('入队稳定去重，仅保后端真实逐项状态，不重复计数或接纳外部编号', async () => {
    const selected = Object.freeze([...ids(215), 1, 201]);
    const requests: number[][] = [];
    const progress: ReanalysisProgress[] = [];
    const api = createApiClient({ fetch: async (request) => {
      const body: Schema<'FindingBatchRequest'> = await request.json();
      expect(body.action).toBe('reanalyze');
      requests.push(body.ids);
      const response = result(body.ids);
      if (requests.length === 1) {
        response.items = body.ids.map((finding_id, index) => ({ finding_id, status: index === 1 ? 'cached' : index === 2 ? 'rejected' : 'queued', needs_detail: true, reason: null }));
        response.items.push(...response.items.slice(0, 1), { finding_id: 9999, status: 'queued', needs_detail: false, reason: null });
      }
      return json(response, 202);
    } });
    const response = await queueReanalysisBatches(api, selected, new AbortController().signal, (value) => progress.push(value));
    expect(requests.map((batch) => batch.length)).toEqual([200, 15]);
    expect(requests.flat()).toEqual(ids(215));
    expect(response.items).toHaveLength(215);
    expect(response.items.filter((item) => item.status === 'queued')).toHaveLength(213);
    expect(response.needs_detail_count).toBe(215);
    expect(progress.at(-1)).toEqual({ items: response.items, total: 215, uncertain: 0, remaining: 0, stopped: false });
  });

  test('第二批503保留第一批200项，区分当前不明200与后续未提交15，抛原错误不重试', async () => {
    const progress: ReanalysisProgress[] = [];
    const failure = new ApiError(503, { code: 'web.network_failed', args: {} }, [], 'second-batch');
    let calls = 0;
    const api = createApiClient({ fetch: async (request) => {
      calls++;
      const body: Schema<'FindingBatchRequest'> = await request.json();
      return calls === 2 ? json({}, 503) : json(result(body.ids), 202);
    } });
    api.use({ onResponse({ response }) { if (response.status === 503) throw failure; } });
    await expect(queueReanalysisBatches(api, ids(415), new AbortController().signal, (value) => progress.push(value))).rejects.toBe(failure);
    expect(calls).toBe(2);
    expect(progress.at(-1)?.items).toHaveLength(200);
    expect(progress.at(-1)).toMatchObject({ total: 415, uncertain: 200, remaining: 15, stopped: true });
    expect(progress.at(-1)?.items.every((item) => item.status === 'queued')).toBe(true);
  });

  test('入队取消立即报告已确认批和当前不明批，迟到响应不能算成功', async () => {
    const progress: ReanalysisProgress[] = [];
    let start: (() => void) | undefined;
    const started = new Promise<void>((resolve) => { start = resolve; });
    let finish: ((response: Response) => void) | undefined;
    let calls = 0;
    const controller = new AbortController();
    const api = createApiClient({ fetch: async (request) => {
      const body: Schema<'FindingBatchRequest'> = await request.json();
      calls++;
      if (calls === 2) return await new Promise<Response>((resolve) => { finish = resolve; start?.(); });
      return json(result(body.ids), 202);
    } });
    const pending = queueReanalysisBatches(api, ids(415), controller.signal, (value) => progress.push(value));
    await started;
    const reason = new DOMException('取消入队', 'AbortError');
    const rejected = pending.then(() => undefined, (value: unknown) => value);
    controller.abort(reason);
    expect(progress.at(-1)?.items).toHaveLength(200);
    expect(progress.at(-1)).toMatchObject({ total: 415, uncertain: 200, remaining: 15, stopped: true });
    finish?.(json(result(ids(400).slice(200)), 202));
    expect(await rejected).toBe(reason);
    expect(calls).toBe(2);
    expect(progress.filter((value) => value.stopped)).toHaveLength(1);
    expect(progress.at(-1)?.items).toHaveLength(200);
  });

  test('第二批401停止后续请求，保留第一批真实事实', async () => {
    const progress: ReanalysisProgress[] = [];
    let calls = 0;
    const controller = new AbortController();
    const api = createApiClient({ fetch: async (request) => {
      calls++;
      const body: Schema<'FindingBatchRequest'> = await request.json();
      return calls === 2 ? json({ error: { code: 'auth.unauthenticated', args: {} }, fields: [], request_id: 'session-lost' } satisfies Schema<'ErrorEnvelope'>, 401) : json(result(body.ids), 202);
    } });
    api.subscribeAuthenticationLost(() => controller.abort());
    const error = await queueReanalysisBatches(api, ids(415), controller.signal, (value) => progress.push(value)).then(() => undefined, (value: unknown) => value);
    if (!(error instanceof ApiError)) throw new Error('未返回接口错误');
    expect(error.status).toBe(401);
    expect(error.requestID).toBe('session-lost');
    expect(controller.signal.aborted).toBe(true);
    expect(calls).toBe(2);
    expect(progress.at(-1)).toMatchObject({ total: 415, uncertain: 200, remaining: 15, stopped: true });
    expect(progress.at(-1)?.items).toHaveLength(200);
  });

  test('成功HTTP响应漏报部分编号时，不把未报项当已确认', async () => {
    const progress: ReanalysisProgress[] = [];
    const api = createApiClient({ fetch: async () => json(result([1, 1, 9999]), 202) });
    const error = await queueReanalysisBatches(api, [1, 2, 3], new AbortController().signal, (value) => progress.push(value)).then(() => undefined, (value: unknown) => value);
    if (!(error instanceof ApiError)) throw new Error('未返回接口错误');
    expect(error.reason.code).toBe('web.invalid_response');
    expect(progress.at(-1)).toMatchObject({ total: 3, uncertain: 2, remaining: 0, stopped: true });
    expect(progress.at(-1)?.items.map((item) => item.finding_id)).toEqual([1]);
  });

  test('开始前取消仍报告全未提交，保留原取消原因且不请求', async () => {
    const progress: ReanalysisProgress[] = [];
    let calls = 0;
    const controller = new AbortController();
    const reason = new DOMException('已取消', 'AbortError');
    controller.abort(reason);
    const api = createApiClient({ fetch: async () => { calls++; return json({}); } });
    await expect(queueReanalysisBatches(api, ids(215), controller.signal, (value) => progress.push(value))).rejects.toBe(reason);
    expect(calls).toBe(0);
    expect(progress.at(-1)).toEqual({ items: [], total: 215, uncertain: 0, remaining: 215, stopped: true });
  });

  test('当前批正在提交时的进度不能把它说成尚未提交', async () => {
    const progress: ReanalysisProgress[] = [];
    let start: (() => void) | undefined;
    const started = new Promise<void>((resolve) => { start = resolve; });
    let finish: ((response: Response) => void) | undefined;
    const api = createApiClient({ fetch: async () => await new Promise<Response>((resolve) => { finish = resolve; start?.(); }) });
    const pending = queueReanalysisBatches(api, [1], new AbortController().signal, (value) => progress.push(value));
    await started;
    expect(progress.at(-1)).toMatchObject({ total: 1, uncertain: 1, remaining: 0, stopped: false });
    finish?.(json(result([1]), 202));
    await pending;
  });
});
