import { useCallback, useEffect, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { downloadBlob } from '../../api/download';
import { ApiError } from '../../api/errors';
import { apiData, useApi, useApiError, useMessageText } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import type { Schema } from '../../api/types';
import { useAuthentication } from '../../app/auth-state';
import { useUnsavedChanges } from '../../app/unsaved-changes';
import { Button, Dialog, InlineMessage, TextField } from '../../components/ui';
import { useTranslation } from '../../i18n';

type TransferState =
  | { kind: 'ready' | 'pending' }
  | { kind: 'error'; error: unknown }
  | { kind: 'uncertain'; error?: unknown }
  | { kind: 'report'; report: Schema<'TaskImportReport'> };

export function TaskTransferDialog({ mode, onClose }: { mode: 'import' | 'export'; onClose(): void }) {
  const { t } = useTranslation();
  const api = useApi();
  const errorText = useApiError();
  const queryClient = useQueryClient();
  const authenticated = useAuthentication()?.authenticated !== false;
  const importing = mode === 'import';
  const [file, setFile] = useState<File | null>(null);
  const [state, setState] = useState<TransferState>({ kind: 'ready' });
  const operation = useRef<AbortController | null>(null);
  const pending = state.kind === 'pending';
  const finished = state.kind === 'report' || state.kind === 'uncertain';
  const refreshTasks = useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: apiKey('tasks') });
    void queryClient.invalidateQueries({ queryKey: apiKey('overview') });
  }, [queryClient]);
  const cancel = useCallback(() => {
    const controller = operation.current;
    if (!controller) return;
    operation.current = null;
    controller.abort();
    setState({ kind: importing ? 'uncertain' : 'ready' });
    if (importing) refreshTasks();
  }, [importing, refreshTasks]);

  useUnsavedChanges(importing && pending);
  useEffect(() => { if (!authenticated) cancel(); }, [authenticated, cancel]);
  useEffect(() => () => {
    const controller = operation.current;
    operation.current = null;
    if (!controller) return;
    controller.abort();
    if (importing) refreshTasks();
  }, [importing, refreshTasks]);

  const submit = async () => {
    if (operation.current || !authenticated || finished || (importing && !file)) return;
    const controller = new AbortController();
    operation.current = controller;
    setState({ kind: 'pending' });
    try {
      if (importing && file) {
        const report = apiData(await api.POST('/api/v1/tasks/import', {
          body: { file },
          bodySerializer(upload) {
            const form = new FormData();
            form.append('file', upload.file);
            return form;
          },
          signal: controller.signal,
        }));
        if (operation.current !== controller || controller.signal.aborted) return;
        setState({ kind: 'report', report });
        if (report.imported > 0) refreshTasks();
      } else {
        const result = await api.GET('/api/v1/tasks/export', { parseAs: 'blob', signal: controller.signal });
        if (operation.current !== controller || controller.signal.aborted) return;
        const blob = apiData(result);
        const mediaType = blob.type.split(';', 1)[0]?.trim().toLowerCase();
        if (result.response.status !== 200 || mediaType !== 'application/json' || blob.size === 0) {
          throw new ApiError(0, { code: 'web.invalid_response', args: {} });
        }
        downloadBlob(blob, result.response.headers.get('Content-Disposition'), 'kakatta-tasks.json');
        onClose();
      }
    } catch (error) {
      if (operation.current !== controller || controller.signal.aborted) return;
      if (importing && (!(error instanceof ApiError) || error.status === 0 || error.status >= 500)) {
        setState({ kind: 'uncertain', error });
        refreshTasks();
      } else {
        setState({ kind: 'error', error });
      }
    } finally {
      if (operation.current === controller) operation.current = null;
    }
  };

  const title = t(importing ? 'tasks.import' : 'tasks.export');
  const failure = state.kind === 'error' || state.kind === 'uncertain' ? state.error : undefined;
  return <Dialog
    open
    onOpenChange={(open) => {
      if (open) return;
      if (operation.current) cancel();
      else onClose();
    }}
    title={title}
    closeLabel={t(pending ? 'common.cancel' : 'common.close')}
  >
    <div className="space-y-lg">
      <p className="text-body text-text-secondary">{t('tasks.import.unit')}</p>
      <p className="text-body text-text-secondary">{t(importing ? 'tasks.import.effect' : 'tasks.export.scope')}</p>
      {importing && !finished && <TextField
        type="file"
        accept="application/json,.json"
        label={t('tasks.file')}
        disabled={pending || !authenticated}
        onChange={(event) => {
          setFile(event.currentTarget.files?.[0] ?? null);
          setState({ kind: 'ready' });
        }}
      />}
      {file && <p className="break-words text-body">{file.name}</p>}
      {failure !== undefined && <InlineMessage tone="danger">
        <p>{errorText(failure)}</p>
        {failure instanceof ApiError && failure.requestID && <p>{t('web.request_id', { request_id: failure.requestID })}</p>}
      </InlineMessage>}
      {state.kind === 'uncertain' && <InlineMessage tone="warning">{t('tasks.import.outcome_unknown')}</InlineMessage>}
      {state.kind === 'report' && <ImportReport report={state.report} />}
      {!finished && <Button variant="primary" busy={pending} busyLabel={t('common.loading')} disabled={!authenticated || (importing && !file)} onClick={() => { void submit(); }}>{title}</Button>}
    </div>
  </Dialog>;
}

const itemKeys = {
  imported: 'tasks.import.item_imported',
  skipped: 'tasks.import.item_skipped',
  failed: 'tasks.import.item_failed',
} as const;

function ImportReport({ report }: { report: Schema<'TaskImportReport'> }) {
  const { t } = useTranslation();
  const messageText = useMessageText();
  const { total, imported, skipped, failed, unprocessed } = report;
  return <div className="space-y-lg">
    <InlineMessage tone={report.complete && skipped === 0 ? 'positive' : 'warning'}>
      <p>{t('tasks.import.report', { total, imported, skipped, failed, unprocessed })}</p>
      {!report.complete && <p>{t('tasks.import.partial')}</p>}
      {report.failure && <p>{messageText(report.failure)}</p>}
    </InlineMessage>
    {report.items.length > 0 && <ol className="min-w-0 divide-y divide-border">
      {report.items.map((item) => <li key={item.index} className="min-w-0 space-y-sm py-md text-body">
        <p className="font-medium">{t(itemKeys[item.status], { index: item.index })}{item.task_id !== null && <> · #{item.task_id}</>}</p>
        {item.reason && <p>{messageText(item.reason)}</p>}
        {[item.corrections, item.warnings, item.fields].map((issues, group) => issues.length > 0 && <ul key={group} className="space-y-sm text-text-secondary">
          {issues.map((issue, index) => <li key={`${issue.key}-${index}`} className="break-words">
            {issue.key && <><code className="break-all">{issue.key}</code> · </>}{messageText(issue.reason)}
          </li>)}
        </ul>)}
      </li>)}
    </ol>}
  </div>;
}
