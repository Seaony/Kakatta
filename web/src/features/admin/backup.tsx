import { useEffect, useRef, useState } from 'react';
import { Link } from '@tanstack/react-router';
import { useQueryClient } from '@tanstack/react-query';
import { downloadBlob } from '../../api/download';
import { ApiError } from '../../api/errors';
import { apiData, useApi } from '../../api/hooks';
import type { Schema } from '../../api/types';
import { useAuthentication } from '../../app/auth-state';
import { Button, Dialog, FormSection, InlineMessage, PageHeader, TextField } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { formatDateTime } from '../../i18n/format';
import { ErrorNotice, fieldError, useAdminTime, usePrivateAction } from './shared';

export function BackupPage() {
  const time = useAdminTime();
  return <BackupContents time={time} />;
}

export function InitialBackupRestore({ onCancel }: { onCancel(): void }) {
  const { t, locale } = useTranslation();
  return <BackupContents initialSetup onCancel={onCancel} time={(at) => at === null ? t('admin.unknown') : `${formatDateTime(at, locale, 'UTC')} (UTC)`} />;
}

function BackupContents({ time, initialSetup = false, onCancel }: { time(at: number | null): string; initialSetup?: boolean; onCancel?: () => void }) {
  const { t } = useTranslation();
  const api = useApi();
  const queryClient = useQueryClient();
  const authentication = useAuthentication();
  const exporting = usePrivateAction();
  const preparing = usePrivateAction();
  const restoring = usePrivateAction();
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [restorePassword, setRestorePassword] = useState('');
  const [file, setFile] = useState<File | null>(null);
  const [preview, setPreview] = useState<Schema<'BackupRestorePreview'> | null>(null);
  const [expired, setExpired] = useState(false);
  const [exported, setExported] = useState(false);
  const [restored, setRestored] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const restoreError = restoring.error;
  const restoreUnauthorized = restoreError instanceof ApiError && restoreError.status === 401;
  const identityLost = (!initialSetup && authentication?.authenticated === false) || restoreUnauthorized || [exporting.error, preparing.error].some((error) => error instanceof ApiError && error.status === 401);
  const busy = exporting.pending || preparing.pending || restoring.pending;
  const disabled = identityLost || busy || preview !== null;
  const mismatch = confirm.length > 0 && password !== confirm;
  const uncertain = !!restoreError && (!(restoreError instanceof ApiError) || restoreError.status === 0 || restoreError.status >= 500);

  function clearRestoreInput() {
    setRestorePassword('');
    setFile(null);
    if (fileInput.current) fileInput.current.value = '';
  }

  useEffect(() => {
    if (!identityLost) return;
    if (initialSetup) void queryClient.invalidateQueries({ queryKey: ['api', 'setup'] });
    setPassword('');
    setConfirm('');
    clearRestoreInput();
    setPreview(null);
    setExpired(false);
    exporting.cancel();
    preparing.cancel();
    // 恢复本身会使旧会话失效，已经发出的确认仍需等到明确响应。
    if (restoreUnauthorized) restoring.cancel();
  }, [identityLost, restoreUnauthorized]);

  useEffect(() => {
    if (!preview) return;
    const timer = setTimeout(() => {
      setPreview(null);
      setExpired(true);
    }, Math.max(0, preview.expires_at - Date.now()));
    return () => clearTimeout(timer);
  }, [preview]);

  function exportBackup() {
    if (disabled || !password || password !== confirm) return;
    setExported(false);
    void exporting.run(async (signal) => {
      const result = await api.POST('/api/v1/backup/export', { signal, body: { password }, parseAs: 'blob' });
      const blob = apiData(result);
      if (result.response.status !== 200 || blob.type.split(';', 1)[0] !== 'application/zip' || blob.size === 0) {
        throw new ApiError(0, { code: 'web.invalid_response', args: {} }, [], result.response.headers.get('X-Request-Id') ?? '');
      }
      return { blob, disposition: result.response.headers.get('Content-Disposition') };
    }, ({ blob, disposition }) => {
      downloadBlob(blob, disposition, 'kakatta-backup.zip');
      setPassword('');
      setConfirm('');
      setExported(true);
    });
  }

  function prepareRestore() {
    if (disabled || !file || !restorePassword) return;
    setExpired(false);
    restoring.cancel();
    void preparing.run(async (signal) => {
      const result = await api.POST('/api/v1/backup/restore', {
        signal,
        body: { file, password: restorePassword },
        bodySerializer: (input) => {
          const data = new FormData();
          data.set('file', input.file);
          data.set('password', input.password);
          return data;
        },
      });
      const value = apiData(result);
      if (result.response.status !== 200 || !value || typeof value.ticket !== 'string' || !value.ticket ||
        !Number.isSafeInteger(value.expires_at) || !Number.isFinite(new Date(value.expires_at).valueOf()) ||
        typeof value.manifest?.app_version !== 'string' || !Number.isSafeInteger(value.manifest.schema_version) ||
        !Number.isFinite(Date.parse(value.manifest.created_at))) {
        throw new ApiError(0, { code: 'web.invalid_response', args: {} }, [], result.response.headers.get('X-Request-Id') ?? '');
      }
      return value;
    }, (value) => {
      clearRestoreInput();
      setPreview(value);
    });
  }

  function confirmRestore() {
    if (!preview || identityLost || restoring.pending || restoring.blocked) return;
    setPreview(null);
    if (preview.expires_at <= Date.now()) { setExpired(true); return; }
    const ticket = preview.ticket;
    void restoring.run(async (signal) => {
      const result = await api.POST('/api/v1/backup/confirm', { signal, body: { ticket } });
      const value = apiData(result);
      if (result.response.status !== 200 || value?.restored !== true) {
        throw new ApiError(0, { code: 'web.invalid_response', args: {} }, [], result.response.headers.get('X-Request-Id') ?? '');
      }
    }, () => {
      setPassword('');
      setConfirm('');
      clearRestoreInput();
      setRestored(true);
      queryClient.clear();
      window.location.replace('/login');
    });
  }

  if (restored) return <div className="min-w-0 space-y-lg">
    <PageHeader title={t('admin.backup.title')} />
    <InlineMessage tone="positive" action={<a href="/login" className="inline-flex min-h-control items-center underline">{t('admin.sign_in')}</a>}>
      {t('admin.backup.confirmed')}
    </InlineMessage>
  </div>;

  return <div className="min-w-0 space-y-xxl">
    {onCancel && <Button onClick={onCancel} disabled={busy}>{t('web.setup.back_to_account')}</Button>}
    <PageHeader title={t('admin.backup.title')} description={t('admin.backup.description')} />
    {!initialSetup && <>
      <FormSection title={t('admin.backup.export')} description={t('admin.backup.password_reason')}>
        <form className="max-w-prose space-y-lg" onSubmit={(event) => { event.preventDefault(); exportBackup(); }}>
          <TextField type="password" label={t('admin.backup.password')} autoComplete="new-password" value={password} onChange={(event) => { setPassword(event.target.value); setExported(false); }} required disabled={disabled} {...fieldError(exporting.field('password'))} />
          <TextField type="password" label={t('admin.backup.confirm_password')} autoComplete="new-password" value={confirm} onChange={(event) => { setConfirm(event.target.value); setExported(false); }} required disabled={disabled} {...fieldError(mismatch ? t('admin.password.mismatch') : undefined)} />
          <ErrorNotice error={exporting.error} />
          {exported && <InlineMessage tone="positive">{t('admin.backup.export_started')}</InlineMessage>}
          <div className="flex flex-wrap gap-sm">
            <Button type="submit" variant="primary" busy={exporting.pending} busyLabel={t('admin.backup.exporting')} disabled={disabled || exporting.blocked || !password || !confirm || mismatch}>{t('admin.backup.export_action')}</Button>
            {exporting.pending && <Button onClick={exporting.cancel}>{t('common.cancel')}</Button>}
          </div>
        </form>
      </FormSection>
      <FormSection title={t('admin.backup.contents')}>
        <dl className="max-w-prose space-y-lg text-body"><div className="space-y-sm"><dt className="font-medium">{t('admin.backup.included')}</dt><dd className="text-text-secondary">{t('admin.backup.included_detail')}</dd></div><div className="space-y-sm"><dt className="font-medium">{t('admin.backup.excluded')}</dt><dd className="text-text-secondary">{t('admin.backup.excluded_detail')}</dd></div></dl>
        <div className="flex flex-wrap gap-lg"><Link to="/tasks" className="inline-flex min-h-control items-center text-body text-accent-text underline">{t('admin.backup.task_export')}</Link><Link to="/results" className="inline-flex min-h-control items-center text-body text-accent-text underline">{t('admin.backup.csv_export')}</Link><Link to="/logs" className="inline-flex min-h-control items-center text-body text-accent-text underline">{t('admin.backup.log_export')}</Link></div>
      </FormSection>
    </>}
    <FormSection title={t('admin.backup.restore')} description={t('admin.backup.restore_description')}>
      <form className="max-w-prose space-y-lg" onSubmit={(event) => { event.preventDefault(); prepareRestore(); }}>
        <TextField ref={fileInput} type="file" label={t('admin.backup.file')} accept=".zip,application/zip" onChange={(event) => { setFile(event.target.files?.item(0) ?? null); setExpired(false); }} required disabled={disabled} {...fieldError(preparing.field('file'))} />
        {file && <p className="break-all text-body" role="status">{t('admin.backup.selected', { name: file.name })}</p>}
        <TextField type="password" label={t('admin.backup.restore_password')} autoComplete="off" value={restorePassword} onChange={(event) => setRestorePassword(event.target.value)} required disabled={disabled} hint={t('admin.backup.password_distinction')} {...fieldError(preparing.field('password'))} />
        <ErrorNotice error={preparing.error} />
        {expired && <InlineMessage tone="warning">{t('backup.restore_expired')}</InlineMessage>}
        <div className="flex flex-wrap gap-sm">
          <Button type="submit" busy={preparing.pending} busyLabel={t('admin.backup.validating')} disabled={disabled || preparing.blocked || !file || !restorePassword}>{t('admin.backup.validate')}</Button>
          {preparing.pending && <Button onClick={preparing.cancel}>{t('common.cancel')}</Button>}
        </div>
      </form>
      {restoring.pending && <InlineMessage tone="info">{t('admin.backup.confirm_pending')}</InlineMessage>}
      <ErrorNotice error={restoreError} />
      {uncertain && <InlineMessage tone="warning">{t('admin.backup.confirm_uncertain')}</InlineMessage>}
      <div className="max-w-prose space-y-lg">
        <InlineMessage tone="warning" title={t('admin.backup.overwrite_title')}>{t('admin.backup.overwrite')}</InlineMessage>
        <ol className="list-decimal space-y-md pl-xl text-body text-text-secondary"><li>{t('admin.backup.step_validate')}</li><li>{t('admin.backup.step_confirm')}</li><li>{t('admin.backup.step_restore')}</li><li>{t('admin.backup.step_sign_in')}</li></ol>
        <p className="text-body text-text-secondary">{t('admin.backup.failure_safe')}</p>
      </div>
    </FormSection>
    {!initialSetup && <FormSection title={t('admin.backup.after_restore')}>
      <ul className="max-w-prose list-disc space-y-md pl-xl text-body"><li>{t('admin.backup.check_data')}</li><li>{t('admin.backup.check_accounts')}</li><li>{t('admin.backup.check_tokens')}</li><li>{t('admin.backup.external_key')}</li></ul>
      <p className="max-w-prose text-body text-text-secondary">{t('admin.backup.unknown_result')}</p>
    </FormSection>}
    <Dialog open={preview !== null} onOpenChange={(open) => { if (!open) setPreview(null); }} title={t('admin.backup.preview_title')} closeLabel={t('common.cancel')}>
      {preview && <div className="space-y-lg">
        <dl className="grid min-w-0 gap-md text-body sm:grid-cols-2">
          <div><dt className="text-text-secondary">{t('admin.created')}</dt><dd>{time(Date.parse(preview.manifest.created_at))}</dd></div>
          <div><dt className="text-text-secondary">{t('admin.backup.preview_app_version')}</dt><dd className="break-all">{preview.manifest.app_version}</dd></div>
          <div><dt className="text-text-secondary">{t('admin.backup.preview_schema_version')}</dt><dd>{preview.manifest.schema_version}</dd></div>
          <div><dt className="text-text-secondary">{t('admin.expires')}</dt><dd>{time(preview.expires_at)}</dd></div>
        </dl>
        <InlineMessage tone="warning" title={t('admin.backup.overwrite_title')}>
          <p>{t('admin.backup.overwrite')}</p>
          <p className="mt-sm">{t('admin.backup.confirm_usage')}</p>
          <p className="mt-sm">{t('admin.backup.step_sign_in')}</p>
        </InlineMessage>
        <Button variant="destructive" onClick={confirmRestore} disabled={restoring.blocked}>{t('admin.backup.confirm_action')}</Button>
      </div>}
    </Dialog>
  </div>;
}
