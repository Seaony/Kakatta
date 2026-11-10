import { useRef, useState } from 'react';
import { useApiError } from '../../api/hooks';
import { useAuthentication } from '../../app/auth-state';
import { Button, Dialog, InlineMessage, PageHeader, Skeleton, StatePanel } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { AccountForm } from './account-form';
import { AccountStatus, AccountWorkspace } from './account-workspace';
import { useAccounts, uniqueByID } from './data';
import { ProxiesSection } from './proxies';

export type AccountsPageProps = { accountId?: number; onAccountChange?(id: number | undefined): void };

export function AccountsPage({ accountId, onAccountChange }: AccountsPageProps) {
  const { t } = useTranslation();
  const describe = useApiError();
  const auth = useAuthentication();
  const accounts = useAccounts();
  const [localID, setLocalID] = useState<number>();
  const [adding, setAdding] = useState(false);
  const dialogOpen = useRef(false);
  const showAdd = (value: boolean) => { dialogOpen.current = value; setAdding(value); };
  const selected = onAccountChange ? accountId : accountId ?? localID;
  const select = (id: number | undefined) => onAccountChange ? onAccountChange(id) : setLocalID(id);
  const items = uniqueByID(accounts.data?.pages.flatMap((page) => page.items) ?? []);

  return <fieldset disabled={auth?.authenticated === false} className="min-w-0 space-y-xxl border-0 p-0">
    <PageHeader title={t('accounts.title')} description={t('accounts.description')} actions={<Button variant="primary" onClick={() => showAdd(true)}>{t('accounts.add')}</Button>} />
    <div className="grid min-w-0 gap-xl lg:grid-cols-[minmax(16rem,22rem)_minmax(0,1fr)]">
      <section className={selected === undefined ? 'min-w-0 space-y-lg' : 'hidden min-w-0 space-y-lg lg:block'} aria-label={t('accounts.title')}>
        {accounts.isPending ? <StatePanel kind="pending" label={t('common.loading')}><Skeleton className="h-[8rem]" /></StatePanel>
          : accounts.error && !accounts.data ? <StatePanel kind="error" title={t('web.request_failed')} description={describe(accounts.error)} retry={{ label: t('common.retry'), run: () => { void accounts.refetch(); } }} />
            : items.length === 0 ? <StatePanel kind="empty" title={t('accounts.empty')} action={<Button onClick={() => showAdd(true)}>{t('accounts.add')}</Button>} />
              : <ul className="space-y-sm">{items.map((account) => <li key={account.id}><Button className="w-full justify-between gap-lg text-left"
                variant={selected === account.id ? 'secondary' : 'plain'} aria-current={selected === account.id ? true : undefined}
                onClick={() => select(account.id)}><span className="min-w-0 break-words">{account.name}</span><AccountStatus account={account} /></Button></li>)}</ul>}
        {accounts.data && accounts.error && <InlineMessage tone="warning" action={<Button onClick={() => { void accounts.refetch(); }}>{t('common.retry')}</Button>}>{describe(accounts.error)}</InlineMessage>}
        {accounts.hasNextPage && <Button busy={accounts.isFetchingNextPage} onClick={() => { void accounts.fetchNextPage(); }}>{t('accounts.more')}</Button>}
      </section>
      <div className="min-w-0">{selected === undefined ? <StatePanel kind="empty" title={t('accounts.choose')} /> : <>
        <Button className="mb-lg lg:hidden" onClick={() => select(undefined)}>{t('accounts.back')}</Button>
        <AccountWorkspace key={selected} accountId={selected} onDeleted={() => select(undefined)} />
      </>}</div>
    </div>
    <ProxiesSection />
    <Dialog open={adding} onOpenChange={showAdd} title={t('accounts.add')} closeLabel={t('common.close')}>
      <AccountForm onSaved={(account) => { if (dialogOpen.current) { showAdd(false); select(account.id); } }} />
    </Dialog>
  </fieldset>;
}
