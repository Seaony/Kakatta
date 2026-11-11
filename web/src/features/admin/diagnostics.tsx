import { useEffect, useState } from 'react';
import { downloadBlob } from '../../api/download';
import { ApiError } from '../../api/errors';
import { apiData, useApi, useMessageText } from '../../api/hooks';
import type { Schema } from '../../api/types';
import { useAuthentication } from '../../app/auth-state';
import { Button, Dialog, StatePanel } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { ErrorNotice, usePrivateAction } from './shared';

export function DiagnosticsExport() {
  const { t, locale } = useTranslation();
  const api = useApi();
  const message = useMessageText();
  const authentication = useAuthentication();
  const preparing = usePrivateAction();
  const exporting = usePrivateAction();
  const [open, setOpen] = useState(false);
  const [manifest, setManifest] = useState<Schema<'DiagnosticManifest'> | null>(null);
  const identityLost = authentication?.authenticated === false || [preparing.error, exporting.error].some((error) => error instanceof ApiError && error.status === 401);
  const size = (bytes: number) => new Intl.NumberFormat(locale, { style: 'unit', unit: 'byte' }).format(bytes);

  function close() {
    preparing.cancel();
    exporting.cancel();
    setManifest(null);
    setOpen(false);
  }
  useEffect(() => { if (identityLost) close(); }, [identityLost]);

  function prepare() {
    if (identityLost || preparing.pending || exporting.pending) return;
    setOpen(true);
    void preparing.run(async (signal) => apiData(await api.GET('/api/v1/system/diagnostics', { signal })), setManifest);
  }

  function download() {
    if (!manifest?.files.length || identityLost || exporting.pending) return;
    void exporting.run(async (signal) => {
      const result = await api.GET('/api/v1/system/diagnostics/download', { signal, parseAs: 'blob' });
      const blob = apiData(result);
      if (result.response.status !== 200 || blob.type.split(';', 1)[0] !== 'application/zip' || blob.size === 0) {
        throw new ApiError(0, { code: 'web.invalid_response', args: {} }, [], result.response.headers.get('X-Request-Id') ?? '');
      }
      return { blob, disposition: result.response.headers.get('Content-Disposition') };
    }, ({ blob, disposition }) => { downloadBlob(blob, disposition, 'kakatta-diagnostics.zip'); close(); });
  }

  return <>
    <Button onClick={prepare} disabled={identityLost}>{t('admin.diagnostics.export')}</Button>
    <Dialog open={open} onOpenChange={(value) => { if (!value) close(); }} title={t('admin.diagnostics.preview')} description={t('admin.diagnostics.preview_description')} closeLabel={t('common.close')}>
      <div className="min-w-0 space-y-lg">
        {preparing.pending && <StatePanel kind="pending" label={t('common.loading')} />}
        <ErrorNotice error={preparing.error} />
        {!!preparing.error && <Button onClick={prepare}>{t('common.retry')}</Button>}
        {manifest && (manifest.files.length ? <>
          <p className="text-body">{t('admin.diagnostics.total', { count: manifest.files.length, size: size(manifest.total_bytes) })}</p>
          <ul className="divide-y divide-border">{manifest.files.map((file) => <li key={file.path} className="min-w-0 space-y-xs py-sm"><p className="break-all text-body">{file.path}</p><p className="text-caption tabular-nums text-text-secondary">{size(file.bytes)}</p></li>)}</ul>
        </> : <StatePanel kind="empty" title={t('admin.diagnostics.empty')} />)}
        {!!manifest?.excluded.length && <section className="space-y-sm" aria-label={t('admin.diagnostics.excluded')}><h3 className="text-body font-medium">{t('admin.diagnostics.excluded')}</h3><ul className="space-y-md">{manifest.excluded.map((file) => <li key={file.path} className="min-w-0 space-y-xs"><p className="break-all text-body">{file.path}</p><p className="text-body text-warning">{message(file.reason)}</p></li>)}</ul></section>}
        <ErrorNotice error={exporting.error} />
        <div className="flex flex-wrap justify-end gap-sm"><Button onClick={close}>{t('common.cancel')}</Button><Button variant="primary" onClick={download} busy={exporting.pending} disabled={!manifest?.files.length || identityLost || preparing.pending}>{t('admin.diagnostics.download')}</Button></div>
      </div>
    </Dialog>
  </>;
}
