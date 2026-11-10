import type { ApiClient } from '../../api/client';
import { ApiError } from '../../api/errors';
import { apiData } from '../../api/hooks';
import type { Schema } from '../../api/types';

export type ReanalysisProgress = {
  items: readonly Schema<'ReanalysisItem'>[];
  total: number;
  uncertain: number;
  remaining: number;
  stopped: boolean;
};

function snapshotIDs(ids: readonly number[]) {
  const unique = [...new Set(ids)];
  if (unique.length === 0) throw new ApiError(0, { code: 'web.invalid_response', args: {} });
  return unique;
}

export async function previewReanalysisBatches(api: ApiClient, findingIDs: readonly number[], signal: AbortSignal): Promise<Schema<'ReanalysisPreview'>> {
  signal.throwIfAborted();
  const ids = snapshotIDs(findingIDs);
  const preview: Schema<'ReanalysisPreview'> = { as_of: 0, requested_count: 0, needs_detail_count: 0, cached_detail_count: 0 };
  for (let offset = 0; offset < ids.length; offset += 200) {
    signal.throwIfAborted();
    const response = apiData(await api.POST('/api/v1/findings/reanalysis-preview', { body: { ids: ids.slice(offset, offset + 200) }, signal }));
    signal.throwIfAborted();
    preview.as_of = response.as_of;
    preview.requested_count += response.requested_count;
    preview.needs_detail_count += response.needs_detail_count;
    preview.cached_detail_count += response.cached_detail_count;
  }
  return preview;
}

export async function queueReanalysisBatches(api: ApiClient, findingIDs: readonly number[], signal: AbortSignal, onProgress: (progress: ReanalysisProgress) => void): Promise<Schema<'ReanalysisResult'>> {
  const ids = snapshotIDs(findingIDs);
  const known = new Map<number, Schema<'ReanalysisItem'>>();
  let uncertain = 0;
  let submitted = 0;
  let needsDetail = 0;
  let stopped = false;
  const report = (stop: boolean) => {
    if (stopped) return;
    stopped = stop;
    onProgress({ items: [...known.values()], total: ids.length, uncertain, remaining: ids.length - submitted, stopped });
  };
  const abort = () => report(true);
  signal.addEventListener('abort', abort, { once: true });
  try {
    signal.throwIfAborted();
    report(false);
    for (let offset = 0; offset < ids.length; offset += 200) {
      signal.throwIfAborted();
      const batch = ids.slice(offset, offset + 200);
      uncertain = batch.length;
      submitted += batch.length;
      report(false);
      const response = apiData(await api.POST('/api/v1/findings/batch', { body: { ids: batch, action: 'reanalyze' }, signal }));
      signal.throwIfAborted();
      if (!('needs_detail_count' in response)) throw new ApiError(0, { code: 'web.invalid_response', args: {} });
      for (const item of response.items) if (batch.includes(item.finding_id)) known.set(item.finding_id, item);
      needsDetail += response.needs_detail_count;
      uncertain = batch.filter((id) => !known.has(id)).length;
      report(false);
      if (uncertain > 0) throw new ApiError(0, { code: 'web.invalid_response', args: {} });
    }
    return { items: [...known.values()], needs_detail_count: needsDetail };
  } catch (error) {
    report(true);
    throw error;
  } finally {
    signal.removeEventListener('abort', abort);
  }
}
