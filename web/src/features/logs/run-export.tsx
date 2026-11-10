import { useCallback, useEffect, useRef, useState } from 'react';
import { downloadBlob } from '../../api/download';
import { ApiError } from '../../api/errors';
import { useApi } from '../../api/hooks';
import type { RequestQuery } from '../../api/types';
import { Button, Dialog, InlineMessage } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { useLogError, useLogFormat } from './display';

type ExportScope = RequestQuery<'exportRuns'>;
type ExportStatus = 'idle' | 'pending' | 'started' | 'cancelled' | { error: unknown };

export function RunExportDialog({ scope, timezone, retention, onClose }: {
  scope: ExportScope | null;
  timezone: string | null;
  retention: string | null;
  onClose(): void;
}) {
  const api = useApi();
  const { t } = useTranslation();
  const format = useLogFormat(timezone);
  const errorText = useLogError();
  const request = useRef<AbortController | null>(null);
  const [status, setStatus] = useState<ExportStatus>('idle');

  const abortRequest = useCallback(() => {
    const controller = request.current;
    request.current = null;
    controller?.abort();
  }, []);

  const close = useCallback(() => {
    abortRequest();
    setStatus('idle');
    onClose();
  }, [abortRequest, onClose]);

  useEffect(() => {
    const unsubscribe = api.subscribeAuthenticationLost(close);
    return () => {
      unsubscribe();
      abortRequest();
    };
  }, [api, abortRequest, close]);

  async function exportRuns(query: ExportScope) {
    if (request.current) return;
    const controller = new AbortController();
    request.current = controller;
    setStatus('pending');
    try {
      const { data, response } = await api.GET('/api/v1/runs/export', {
        params: { query },
        parseAs: 'blob',
        signal: controller.signal,
      });
      if (request.current !== controller) return;
      const mediaType = response.headers.get('Content-Type')?.split(';', 1)[0]?.trim().toLowerCase();
      if (response.status !== 200 || mediaType !== 'text/plain' || !(data instanceof Blob) || data.size === 0) {
        throw new ApiError(response.status, { code: 'web.invalid_response', args: {} }, [], response.headers.get('X-Request-Id') ?? '');
      }
      downloadBlob(data, response.headers.get('Content-Disposition'), 'kakatta-runs.txt');
      setStatus('started');
    } catch (error) {
      if (request.current === controller) setStatus({ error });
    } finally {
      if (request.current === controller) request.current = null;
    }
  }

  const pending = status === 'pending';
  return (
    <Dialog open={scope !== null} onOpenChange={(open) => { if (!open) close(); }} title={t('logs.export')} closeLabel={t('common.close')}>
      {scope && <div className="space-y-lg">
        <p className="text-body">{t('logs.export.scope')}</p>
        <h2 className="text-body font-medium">{t('logs.export.summary')}</h2>
        <dl className="space-y-sm text-body">
          <div><dt className="text-text-secondary">{t('logs.task')}</dt><dd>{scope.task_id === undefined ? t('logs.all_tasks') : t('logs.task_number', { id: String(scope.task_id) })}</dd></div>
          <div><dt className="text-text-secondary">{t('logs.outcome')}</dt><dd>{scope.outcome ? t(`logs.outcome.${scope.outcome}`) : t('logs.all_outcomes')}</dd></div>
          {scope.failure_kind && <div><dt className="text-text-secondary">{t('logs.failure')}</dt><dd>{t(`logs.failure.${scope.failure_kind}`)}</dd></div>}
          {scope.q && <div><dt className="text-text-secondary">{t('logs.search')}</dt><dd className="break-words">{scope.q}</dd></div>}
          {scope.from_at !== undefined && <div><dt className="text-text-secondary">{t('logs.started')}</dt><dd>{format.time(scope.from_at)}</dd></div>}
          {scope.to_at !== undefined && <div><dt className="text-text-secondary">{t('logs.ended')}</dt><dd>{format.time(scope.to_at)}</dd></div>}
        </dl>
        {retention && <p className="text-body text-text-secondary">{retention}</p>}
        {status === 'started' && <InlineMessage tone="positive">{t('transfer.download.started')}</InlineMessage>}
        {status === 'cancelled' && <InlineMessage>{t('transfer.download.cancelled')}</InlineMessage>}
        {typeof status === 'object' && <InlineMessage tone="danger">{errorText(status.error)}</InlineMessage>}
        <div className="flex flex-wrap gap-sm">
          <Button variant="primary" busy={pending} busyLabel={t('transfer.download.preparing')} onClick={() => void exportRuns(scope)}>
            {t(typeof status === 'object' ? 'common.retry' : 'logs.export')}
          </Button>
          {pending && <Button onClick={() => { abortRequest(); setStatus('cancelled'); }}>{t('common.cancel')}</Button>}
        </div>
      </div>}
    </Dialog>
  );
}
