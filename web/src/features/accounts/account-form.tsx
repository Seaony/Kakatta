import { useEffect, useRef, useState } from 'react';
import { apiData, useApi, useApiError } from '../../api/hooks';
import type { Schema } from '../../api/types';
import { useAuthentication } from '../../app/auth-state';
import { Button, InlineMessage, SelectField, TextField } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { useAccountCache, useOperation, useProxies, uniqueByID } from './data';
import type { Account } from './data';

export function AccountForm({ account, onSaved }: { account?: Account; onSaved(account: Account): void }) {
  const { t } = useTranslation();
  const api = useApi();
  const auth = useAuthentication();
  const describe = useApiError();
  const cache = useAccountCache();
  const proxies = useProxies();
  const operation = useOperation();
  const form = useRef<HTMLFormElement>(null);
  const [name, setName] = useState(account?.name ?? '');
  const [proxy, setProxy] = useState(account?.proxy_id?.toString() ?? '');
  const nameError = operation.fieldError('name');
  const proxyError = operation.fieldError('proxy_id');
  const items = uniqueByID(proxies.data?.pages.flatMap((page) => page.items) ?? []);
  const options = [{ value: '', label: t('accounts.proxy_none') }, ...items.map((item) => ({ value: String(item.id), label: item.name }))];
  if (proxy && !items.some((item) => String(item.id) === proxy)) options.push({ value: proxy, label: `${t('accounts.proxy')} #${proxy}` });

  useEffect(() => {
    if (operation.error) form.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
  }, [operation.error]);

  return <form ref={form} className="space-y-lg" onSubmit={(event) => {
    event.preventDefault();
    const body: Schema<'AccountCreate'> = { name: name.trim(), proxy_id: proxy ? Number(proxy) : null };
    void operation.run(async () => account
      ? apiData(await api.PATCH('/api/v1/accounts/{account_id}', { params: { path: { account_id: account.id } }, body }))
      : apiData(await api.POST('/api/v1/accounts', { body })), (saved) => {
      cache.save(saved);
      cache.changed();
      onSaved(saved);
    });
  }}><fieldset disabled={auth?.authenticated === false} className="min-w-0 space-y-lg border-0 p-0">
    <TextField name="name" label={t('accounts.name')} value={name} required autoComplete="off" disabled={operation.pending}
      onChange={(event) => setName(event.target.value)} {...(nameError ? { error: nameError } : {})} />
    <SelectField name="proxy_id" label={t('accounts.proxy')} value={proxy} options={options} hint={t('accounts.proxy_fixed')}
      disabled={operation.pending || (!proxies.data && proxies.isPending)} onValueChange={setProxy}
      {...(proxyError ? { error: proxyError } : {})} />
    {proxies.error && <InlineMessage tone="danger" action={<Button onClick={() => { void proxies.refetch(); }}>{t('common.retry')}</Button>}>{describe(proxies.error)}</InlineMessage>}
    {proxies.hasNextPage && <div className="space-y-sm"><p className="text-body text-text-secondary">{t('accounts.select_proxy_more')}</p>
      <Button busy={proxies.isFetchingNextPage} onClick={() => { void proxies.fetchNextPage(); }}>{t('proxies.more')}</Button></div>}
    {operation.errorText && <InlineMessage tone="danger">{operation.errorText}</InlineMessage>}
    <Button type="submit" variant="primary" busy={operation.pending}>{t('accounts.save')}</Button>
  </fieldset></form>;
}
