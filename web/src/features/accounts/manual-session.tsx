import { useEffect, useRef } from 'react';
import { apiData, useApi, useApiError } from '../../api/hooks';
import { ApiError } from '../../api/errors';
import { useAuthentication } from '../../app/auth-state';
import { Button, InlineMessage, StatusBadge } from '../../components/ui';
import { useTranslation, type Locale } from '../../i18n';
import { formatDateTime } from '../../i18n/format';
import { useAccount, useAccountCache, useOperation } from './data';
import type { Account, Session } from './data';

export function remotePath(path: string | null, origin: string, locale?: Locale): string | null {
  if (!path?.startsWith('/remote/') || path.startsWith('//')) return null;
  try {
    const url = new URL(path, origin);
    if (url.origin !== origin || !url.pathname.startsWith('/remote/') || url.username || url.password || url.hash) return null;
    if (locale) url.hash = `lang=${locale}`;
    return url.href;
  } catch { return null; }
}

export function ManualSession({ account, session, timeZone, reveal, onReveal, onChanged, onGone }: {
  account: Account;
  session: Session;
  timeZone: string;
  reveal: boolean;
  onReveal(): void;
  onChanged(account: Account): void;
  onGone(): void;
}) {
  const api = useApi();
  const auth = useAuthentication();
  const { t, locale } = useTranslation();
  const describe = useApiError();
  const operation = useOperation();
  const cache = useAccountCache();
  const frame = useRef<HTMLIFrameElement>(null);
  const params = { path: { account_id: account.id, session_id: session.id } };
  const state = useAccount(account.id);
  const current = state.data?.session ?? session;
  const path = remotePath(current.remote_path, globalThis.location.origin, locale);
  const showViewer = auth?.authenticated !== false && reveal && path !== null && current.phase !== 'queued' && current.phase !== 'closing';
  const refreshAfterConfirm = operation.error instanceof ApiError && operation.error.reason.code === 'remote.verification_refresh_confirm';
  const x = current.viewport?.x ?? null;
  const y = current.viewport?.y ?? null;
  const width = current.viewport?.width ?? null;
  const height = current.viewport?.height ?? null;

  useEffect(() => {
    const region = x === null || y === null || width === null || height === null ? null : { x, y, width, height };
    if (showViewer) frame.current?.contentWindow?.postMessage({ type: 'kakatta:viewport', region }, globalThis.location.origin);
  }, [x, y, width, height, showViewer]);

  return <section className="min-w-0 space-y-lg border-t border-border pt-xl" aria-label={t('remote.account_title', { name: account.name, purpose: t(`remote.kind.${current.kind}`) })}>
    <header className="flex flex-wrap items-start justify-between gap-md">
      <div className="min-w-0 space-y-sm"><h3 className="text-section font-semibold">{t(`remote.kind.${current.kind}`)}</h3>
        <StatusBadge tone="info">{t(`remote.phase.${current.phase}`)}</StatusBadge></div>
      <Button busy={state.isFetching} onClick={() => { void state.refetch(); }}>{t('remote.retry_status')}</Button>
    </header>
    {current.phase === 'queued' ? <InlineMessage tone="info">{t('remote.queued')}</InlineMessage> : <p className="max-w-prose text-body text-text-secondary">{t('remote.time_limit')}</p>}
    {current.expires_at !== null && <p className="text-body tabular-nums">{t(current.phase === 'verify_hold' ? 'remote.held_until' : 'remote.expires_at', {
      time: `${formatDateTime(current.expires_at, locale, timeZone)} (${timeZone})`,
    })}</p>}
    {state.error && <InlineMessage tone="warning">{describe(state.error)} {t('remote.status_unknown')}</InlineMessage>}
    {showViewer ? <>
      <p className="max-w-prose text-body text-text-body">{t(current.kind === 'login' ? 'remote.login_help' : 'remote.verification_help')}</p>
      {(refreshAfterConfirm || current.refresh_verification) && <InlineMessage tone="warning">{t(refreshAfterConfirm ? 'remote.verification_refresh_confirm' : 'remote.verification_refresh_page')}</InlineMessage>}
      <iframe ref={frame} src={path} title={t('remote.viewer_title', { name: account.name })}
        className="block h-[min(75vh,52rem)] min-h-[24rem] w-full rounded-control border border-border bg-inset supports-[height:100dvh]:h-[min(75dvh,75vh,52rem)]"
        referrerPolicy="same-origin" onLoad={() => frame.current?.contentWindow?.postMessage({ type: 'kakatta:viewport', region: current.viewport }, globalThis.location.origin)} />
    </> : current.remote_path !== null && !path ? <InlineMessage tone="danger">{t('remote.invalid_path')}</InlineMessage>
      : <p className="text-body text-text-secondary">{t('remote.no_viewer')}</p>}
    {operation.errorText && !(showViewer && refreshAfterConfirm) && <InlineMessage tone="danger">{operation.errorText}</InlineMessage>}
    <div className="flex flex-wrap gap-sm">
      {!reveal && current.phase !== 'queued' && current.phase !== 'closing' && path && <Button variant="primary" onClick={onReveal}>{t('remote.resume')}</Button>}
      {reveal && (current.phase === 'verifying' || current.phase === 'verify_hold') && <Button variant="primary" busy={operation.pending} onClick={() => {
        void operation.run(async () => apiData(await api.POST('/api/v1/accounts/{account_id}/verification/confirm', {
          params: { path: { account_id: account.id } }, body: { session_id: current.id },
        })), (updated) => {
          cache.save(updated); cache.changed(); onChanged(updated);
        });
      }}>{t('remote.confirm_verification')}</Button>}
      <Button busy={operation.pending} disabled={current.phase === 'closing'} onClick={() => {
        void operation.run(async () => { await api.DELETE('/api/v1/accounts/{account_id}/sessions/{session_id}', { params }); }, () => {
          cache.changed(); onGone();
        });
      }}>{t('remote.finish')}</Button>
    </div>
  </section>;
}
