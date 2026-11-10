import { useId, useState } from 'react';
import { downloadBlob } from '../../api/download';
import { ApiError, isCancellation } from '../../api/errors';
import { apiData, useApi, useApiError } from '../../api/hooks';
import type { RequestQuery } from '../../api/types';
import { Button, InlineMessage } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { useCancellableAction } from '../settings-general/use-cancellable-action';

export function FindingsCSVExport({ taskId, query }: { taskId: number | undefined; query: RequestQuery<'exportTaskFindings'> }) {
  const api = useApi();
  const errorText = useApiError();
  const { t } = useTranslation();
  const hintId = useId();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const action = useCancellableAction(() => setBusy(false));

  async function run() {
    if (taskId === undefined) return;
    const request = action.start();
    if (!request) return;
    setBusy(true);
    setError(null);
    try {
      const result = await api.GET('/api/v1/tasks/{task_id}/findings/export', {
        params: { path: { task_id: taskId }, query },
        parseAs: 'blob',
        signal: request.signal,
      });
      if (!action.isCurrent(request)) return;
      const blob = apiData(result);
      const { headers, status } = result.response;
      const media = headers.get('content-type')?.split(';', 1)[0]?.trim().toLowerCase();
      const length = headers.get('content-length');
      const encoding = headers.get('content-encoding')?.trim().toLowerCase();
      const wrongLength = length !== null && (!encoding || encoding === 'identity') && (!/^\d+$/.test(length) || Number(length) !== blob.size);
      if (status !== 200 || media !== 'text/csv' || blob.size === 0 || wrongLength) {
        throw new ApiError(0, { code: 'web.invalid_response', args: {} });
      }
      downloadBlob(blob, headers.get('content-disposition'), `kakatta-results-${taskId}.csv`);
    } catch (failure) {
      if (action.isCurrent(request) && !isCancellation(failure)) setError(failure);
    } finally {
      if (action.finish(request)) setBusy(false);
    }
  }

  return <div className="flex min-w-0 flex-wrap items-center gap-sm" role="group" aria-label={t('results.export.action')}>
    <Button disabled={taskId === undefined || !action.enabled} busy={busy} busyLabel={t('results.export.busy')}
      aria-describedby={taskId === undefined ? hintId : undefined} onClick={() => { void run(); }}>{t('results.export.action')}</Button>
    {taskId === undefined && <p id={hintId} className="text-caption text-text-secondary">{t('results.export.select_task')}</p>}
    {busy && <Button onClick={action.cancel}>{t('common.cancel')}</Button>}
    {error !== null && <InlineMessage tone="danger" className="basis-full">
      <p>{errorText(error)}</p>
      {error instanceof ApiError && error.requestID && <p className="break-all text-caption">{t('web.request_id', { request_id: error.requestID })}</p>}
    </InlineMessage>}
  </div>;
}
