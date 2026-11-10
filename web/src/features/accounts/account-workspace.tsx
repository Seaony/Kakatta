import { useQuery } from '@tanstack/react-query';
import { useCallback, useEffect, useState } from 'react';
import { apiData, useApi, useApiError, useMessageText } from '../../api/hooks';
import { ApiError } from '../../api/errors';
import { apiKey } from '../../api/keys';
import { useAuthentication } from '../../app/auth-state';
import { Button, ConfirmDialog, Dialog, InlineMessage, Skeleton, StatePanel, StatusBadge } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { formatDateTime, formatInteger } from '../../i18n/format';
import { AccountForm } from './account-form';
import { useAccount, useAccountCache, useOperation, useProxies, uniqueByID } from './data';
import type { Account, Session } from './data';
import { ManualSession } from './manual-session';

const tones = { signed_out: 'neutral', active: 'positive', cooling: 'warning', needs_verification: 'warning', denied: 'danger', expired: 'danger' } as const;

export function AccountStatus({ account }: { account: Account }) {
  const { t } = useTranslation();
  return <StatusBadge tone={tones[account.state]}>{t(`accounts.state.${account.state}`)}</StatusBadge>;
}

export function AccountWorkspace({ accountId, onDeleted, onAccount }: {
  accountId: number; onDeleted?(): void; onAccount?(account: Account): void;
}) {
  const api = useApi();
  const auth = useAuthentication();
  const { t, locale } = useTranslation();
  const describe = useApiError();
  const message = useMessageText();
  const accountQuery = useAccount(accountId);
  const proxies = useProxies();
  const cache = useAccountCache();
  const operation = useOperation();
  const [editing, setEditing] = useState(false);
  const [confirm, setConfirm] = useState<'delete' | 'logout' | null>(null);
  const [acceptedSession, setAcceptedSession] = useState<Session | null>(null);
  const [revealed, setRevealed] = useState(false);
  const settings = useQuery({
    enabled: auth?.authenticated !== false,
    queryKey: apiKey('settings'),
    queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings', { signal })),
  });
  const settingZone = settings.data?.values['general.timezone'];
  const timeZone = typeof settingZone === 'string' ? settingZone : 'UTC';
  const account = accountQuery.data;
  const session = acceptedSession ?? account?.session ?? null;
  const sessionGone = useCallback(() => {
    setAcceptedSession(null); setRevealed(false);
    void accountQuery.refetch();
  }, [accountQuery.refetch]);

  useEffect(() => {
    if (account && !accountQuery.error && !accountQuery.isFetching && auth?.authenticated !== false) onAccount?.(account);
  }, [account, accountQuery.error, accountQuery.isFetching, onAccount, auth?.authenticated]);
  useEffect(() => { if (auth?.authenticated === false) setRevealed(false); }, [auth?.authenticated]);

  if (!account || (accountQuery.error instanceof ApiError && accountQuery.error.status === 404 && confirm === null)) {
    if (accountQuery.isPending) return <StatePanel kind="pending" label={t('common.loading')}><Skeleton className="h-[8rem]" /></StatePanel>;
    return <StatePanel kind="error" title={t('web.request_failed')} description={describe(accountQuery.error)} retry={{ label: t('common.retry'), run: () => { void accountQuery.refetch(); } }} />;
  }
  const availableProxies = uniqueByID(proxies.data?.pages.flatMap((page) => page.items) ?? []);
  const nickname = account.nickname?.trim() ? account.nickname : null;
  const proxyName = account.proxy_id === null ? t('accounts.proxy_none')
    : availableProxies.find((proxy) => proxy.id === account.proxy_id)?.name ?? `${t('accounts.proxy')} #${account.proxy_id}`;
  const time = (value: number) => `${formatDateTime(value, locale, timeZone)} (${timeZone})`;
  const gap = account.last_run_ended_at !== null && account.next_gap_seconds !== null
    ? account.last_run_ended_at + account.next_gap_seconds * 1_000 : null;
  const manualSession = session !== null && (session.kind !== 'run' || session.phase === 'verify_hold' || session.phase === 'verifying');
  const open = (kind: 'login' | 'verification') => {
    void operation.run(async () => kind === 'login'
      ? apiData(await api.POST('/api/v1/accounts/{account_id}/login', { params: { path: { account_id: account.id } } }))
      : apiData(await api.POST('/api/v1/accounts/{account_id}/verification', { params: { path: { account_id: account.id } } })), (started) => {
        setAcceptedSession(started); setRevealed(true); cache.changed();
      });
  };

  return <section className="min-w-0 space-y-xl" aria-label={account.name}>
    <header className="flex flex-wrap items-start justify-between gap-md">
      <div className="min-w-0 space-y-sm"><h2 className="break-words text-section font-semibold">{nickname ?? account.name}</h2>
        {nickname && <p className="break-words text-body text-text-secondary">{account.name}</p>}
        <AccountStatus account={account} /></div>
      <Button busy={accountQuery.isFetching} onClick={() => { void accountQuery.refetch(); }}>{t('accounts.refresh')}</Button>
    </header>
    {accountQuery.error && <InlineMessage tone="warning">{describe(accountQuery.error)}</InlineMessage>}
    {settings.error && <InlineMessage tone="warning" action={<Button onClick={() => { void settings.refetch(); }}>{t('common.retry')}</Button>}>{describe(settings.error)}</InlineMessage>}
    {account.state_reason && <InlineMessage tone={account.state === 'denied' || account.state === 'expired' ? 'danger' : 'info'}>{message(account.state_reason)}</InlineMessage>}
    {account.cooldown_until !== null && (account.state === 'cooling' || account.state === 'denied') && (
      <p className="text-body tabular-nums">
        {t(account.relogin_after_pause ? 'accounts.relogin_after_pause' : 'accounts.cooling_until', { time: time(account.cooldown_until) })}
      </p>
    )}
    {account.state === 'cooling' && !account.relogin_after_pause && (
      <p className="max-w-prose text-body text-text-secondary">{t(account.cooldown_manual_used ? 'accounts.manual_used' : 'accounts.manual_once')}</p>
    )}
    <dl className="space-y-md text-body"><div className="flex flex-wrap justify-between gap-md"><dt className="text-text-secondary">{t('accounts.proxy')}</dt><dd className="break-words">{proxyName}</dd></div></dl>
    <p className="max-w-prose text-body text-text-secondary">{t('accounts.proxy_fixed')}</p>
    {gap !== null && Number.isSafeInteger(gap) && <p className="text-body text-text-secondary">{t('accounts.gap', { time: time(gap) })}</p>}
    {operation.errorText && <InlineMessage tone="danger">{operation.errorText}</InlineMessage>}
    {operation.nextAllowedAt !== null && <p className="text-body tabular-nums">{t('accounts.next_allowed', { time: time(operation.nextAllowedAt) })}</p>}
    <div className="flex flex-wrap gap-sm">
      {(account.state === 'signed_out' || account.state === 'expired') && !session && <Button variant="primary" busy={operation.pending} onClick={() => open('login')}>{t(account.state === 'expired' ? 'accounts.relogin' : 'accounts.login')}</Button>}
      {account.state === 'needs_verification' && (!session || session.phase === 'verify_hold') && <Button variant="primary" busy={operation.pending} onClick={() => open('verification')}>{t('accounts.verify')}</Button>}
      <Button onClick={() => setEditing(true)}>{t('accounts.edit')}</Button>
      {account.state !== 'signed_out' && <Button disabled={operation.pending} onClick={() => { operation.clearError(); setConfirm('logout'); }}>{t('accounts.logout')}</Button>}
      <Button variant="plain" disabled={operation.pending} onClick={() => { operation.clearError(); setConfirm('delete'); }}>{t('accounts.delete')}</Button>
    </div>
    {session && !manualSession && <StatusBadge tone="info">{t(`remote.phase.${session.phase}`)}</StatusBadge>}
    {session && manualSession && <ManualSession key={session.id} account={account} session={session} timeZone={timeZone} reveal={revealed}
      onReveal={() => session.phase === 'verify_hold' ? open('verification') : setRevealed(true)}
      onChanged={(updated) => { setAcceptedSession(null); cache.save(updated); if (!updated.session) setRevealed(false); }} onGone={sessionGone} />}
    <section className="space-y-lg border-t border-border pt-xl" aria-label={t('accounts.quota.title')}>
      <h3 className="text-section font-semibold">{t('accounts.quota.title')}</h3>
      <dl className="space-y-md text-body">{([
        ['accounts.quota.hourly', account.quota.hourly_used, account.quota.hourly_limit],
        ['accounts.quota.search', account.quota.daily_search_used, account.quota.daily_search_limit],
        ['accounts.quota.detail', account.quota.daily_detail_used, account.quota.daily_detail_limit],
      ] as const).map(([key, used, limit]) => <div key={key} className="flex flex-wrap justify-between gap-md"><dt className="text-text-secondary">{t(key)}</dt><dd className="tabular-nums">{t('accounts.quota.value', { used: formatInteger(used, locale), limit: formatInteger(limit, locale) })}</dd></div>)}</dl>
      <p className="text-caption text-text-secondary">{t('accounts.quota.as_of', { time: time(account.quota.as_of) })}</p>
    </section>
    <Dialog open={editing} onOpenChange={setEditing} title={t('accounts.edit')} closeLabel={t('common.close')}>
      <AccountForm key={account.id} account={account} onSaved={() => setEditing(false)} />
    </Dialog>
    <ConfirmDialog open={confirm !== null} onOpenChange={(value) => { if (!value && !operation.pending) setConfirm(null); }}
      title={t(confirm === 'delete' ? 'accounts.delete' : 'accounts.logout')}
      description={t(confirm === 'delete' ? 'accounts.delete_effect' : 'accounts.signout_effect', { name: account.name })}
      confirmLabel={t(confirm === 'delete' ? 'accounts.delete' : 'accounts.logout')} cancelLabel={t('common.cancel')}
      pending={operation.pending} {...(operation.errorText ? { error: operation.errorText } : {})} destructive onConfirm={() => {
        if (confirm === 'delete') void operation.run(async () => { await api.DELETE('/api/v1/accounts/{account_id}', { params: { path: { account_id: account.id } } }); }, () => {
          cache.changed(); setConfirm(null); setAcceptedSession(null); onDeleted?.();
        });
        else if (confirm === 'logout') void operation.run(async () => apiData(await api.POST('/api/v1/accounts/{account_id}/logout', { params: { path: { account_id: account.id } } })), (updated) => {
          cache.save(updated); cache.changed(); setConfirm(null); setAcceptedSession(null); setRevealed(false);
        });
      }} />
  </section>;
}
