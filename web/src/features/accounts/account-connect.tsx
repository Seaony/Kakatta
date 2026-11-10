import { useCallback, useRef, useState } from 'react';
import { useApiError } from '../../api/hooks';
import type { Schema } from '../../api/types';
import { useAuthentication } from '../../app/auth-state';
import { Button, InlineMessage, Skeleton, StatePanel } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { AccountForm } from './account-form';
import { AccountStatus, AccountWorkspace } from './account-workspace';
import { uniqueByID, useAccounts } from './data';

export type AccountConnectProps = {
  initialAccountId?: number;
  onConnected(account: Schema<'AccountView'>): void;
};

export function AccountConnect({ initialAccountId, onConnected }: AccountConnectProps) {
  const { t } = useTranslation();
  const describe = useApiError();
  const auth = useAuthentication();
  const accounts = useAccounts();
  const [selected, setSelected] = useState(initialAccountId);
  const notified = useRef<number | undefined>(undefined);
  const ready = useCallback((account: Schema<'AccountView'>) => {
    const hasManualSession = account.session && (account.session.kind !== 'run' || account.session.phase === 'verify_hold' || account.session.phase === 'verifying');
    const signedIn = account.state === 'active' || (account.state === 'cooling' && !account.relogin_after_pause);
    if (signedIn && !hasManualSession && notified.current !== account.id) {
      notified.current = account.id;
      onConnected(account);
    }
  }, [onConnected]);
  const items = uniqueByID(accounts.data?.pages.flatMap((page) => page.items) ?? []);
  if (selected !== undefined) return <fieldset disabled={auth?.authenticated === false} className="min-w-0 space-y-lg border-0 p-0">
    <Button onClick={() => { notified.current = undefined; setSelected(undefined); }}>{t('accounts.back')}</Button>
    <AccountWorkspace key={selected} accountId={selected} onAccount={ready} onDeleted={() => setSelected(undefined)} />
  </fieldset>;

  return <fieldset disabled={auth?.authenticated === false} className="min-w-0 space-y-xl border-0 p-0">
    {accounts.isPending && <StatePanel kind="pending" label={t('common.loading')}><Skeleton /></StatePanel>}
    {accounts.error && <InlineMessage tone="warning" action={<Button onClick={() => { void accounts.refetch(); }}>{t('common.retry')}</Button>}>{describe(accounts.error)}</InlineMessage>}
    {items.length > 0 && <section className="space-y-md" aria-label={t('accounts.choose')}><p className="text-body text-text-secondary">{t('accounts.choose')}</p>
      <ul className="space-y-sm">{items.map((account) => <li key={account.id}><Button className="w-full justify-between gap-lg text-left" onClick={() => setSelected(account.id)}>
        <span className="min-w-0 break-words">{account.name}</span><AccountStatus account={account} />
      </Button></li>)}</ul>
      {accounts.hasNextPage && <Button busy={accounts.isFetchingNextPage} onClick={() => { void accounts.fetchNextPage(); }}>{t('accounts.more')}</Button>}
    </section>}
    <section className="space-y-lg" aria-label={t('accounts.add')}><h2 className="text-section font-semibold">{t('accounts.add')}</h2>
      <AccountForm onSaved={(account) => setSelected(account.id)} />
    </section>
  </fieldset>;
}
