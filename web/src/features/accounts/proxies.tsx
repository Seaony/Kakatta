import { useEffect, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { apiData, useApi, useApiError, useMessageText } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import type { Schema } from '../../api/types';
import { useAuthentication } from '../../app/auth-state';
import { Button, ConfirmDialog, Dialog, InlineMessage, Skeleton, StatePanel, StatusBadge, TextField } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { uniqueByID, useOperation, useProxies } from './data';
import type { Proxy } from './data';

function ProxyForm({ proxy, onSaved }: { proxy?: Proxy; onSaved(proxy: Proxy): void }) {
  const api = useApi();
  const auth = useAuthentication();
  const { t } = useTranslation();
  const operation = useOperation();
  const form = useRef<HTMLFormElement>(null);
  const [name, setName] = useState(proxy?.name ?? '');
  const [url, setURL] = useState('');
  const nameError = operation.fieldError('name');
  const urlError = operation.fieldError('url');

  useEffect(() => {
    if (operation.error) form.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
  }, [operation.error]);
  useEffect(() => { if (auth?.authenticated === false) setURL(''); }, [auth?.authenticated]);

  return <form ref={form} className="space-y-lg" onSubmit={(event) => {
    event.preventDefault();
    const body: Schema<'ProxyPatch'> = { name: name.trim(), ...(url ? { url } : {}) };
    void operation.run(async () => proxy
      ? apiData(await api.PATCH('/api/v1/proxies/{proxy_id}', { params: { path: { proxy_id: proxy.id } }, body }))
      : apiData(await api.POST('/api/v1/proxies', { body: { name: name.trim(), url } })), (saved) => {
        setURL(''); onSaved(saved);
      });
  }}><fieldset disabled={auth?.authenticated === false} className="min-w-0 space-y-lg border-0 p-0">
    <TextField label={t('proxies.name')} name="name" value={name} required disabled={operation.pending} autoComplete="off"
      onChange={(event) => setName(event.target.value)} {...(nameError ? { error: nameError } : {})} />
    <TextField label={t('proxies.url')} name="url" value={url} type="password" required={!proxy?.configured} disabled={operation.pending}
      autoComplete="new-password" autoCapitalize="none" autoCorrect="off" spellCheck={false}
      hint={proxy?.configured ? t('proxies.secret_set') : t('proxies.test_scope')} onChange={(event) => setURL(event.target.value)} {...(urlError ? { error: urlError } : {})} />
    {operation.errorText && <InlineMessage tone="danger">{operation.errorText}</InlineMessage>}
    <Button variant="primary" type="submit" busy={operation.pending}>{t('proxies.save')}</Button>
  </fieldset></form>;
}

export function ProxiesSection() {
  const { t } = useTranslation();
  const api = useApi();
  const queryClient = useQueryClient();
  const describe = useApiError();
  const message = useMessageText();
  const proxies = useProxies();
  const operation = useOperation();
  const [editing, setEditing] = useState<Proxy | 'new' | null>(null);
  const [deleting, setDeleting] = useState<Proxy | null>(null);
  const [testing, setTesting] = useState<number | null>(null);
  const [results, setResults] = useState<Record<number, Schema<'ProxyTestResult'>>>({});
  const items = uniqueByID(proxies.data?.pages.flatMap((page) => page.items) ?? []);
  const changed = () => {
    void queryClient.invalidateQueries({ queryKey: apiKey('proxies') });
    void queryClient.invalidateQueries({ queryKey: apiKey('accounts') });
  };

  return <section className="min-w-0 space-y-lg border-t border-border pt-xl" aria-labelledby="proxies-title">
    <header className="flex flex-wrap items-center justify-between gap-md"><h2 id="proxies-title" className="text-section font-semibold">{t('proxies.title')}</h2>
      <Button onClick={() => setEditing('new')}>{t('proxies.add')}</Button></header>
    <p className="max-w-prose text-body text-text-secondary">{t('proxies.test_scope')}</p>
    {proxies.isPending ? <StatePanel kind="pending" label={t('common.loading')}><Skeleton className="h-[6rem]" /></StatePanel>
      : proxies.error && !proxies.data ? <StatePanel kind="error" title={t('web.request_failed')} description={describe(proxies.error)} retry={{ label: t('common.retry'), run: () => { void proxies.refetch(); } }} />
        : items.length === 0 ? <StatePanel kind="empty" title={t('proxies.empty')} />
          : <ul className="divide-y divide-border">{items.map((proxy) => {
            const result = results[proxy.id];
            return <li key={proxy.id} className="space-y-md py-lg">
            <div className="flex flex-wrap items-center justify-between gap-md"><div className="min-w-0 space-y-sm"><h3 className="break-words text-body font-medium">{proxy.name}</h3>
              <StatusBadge tone={proxy.configured ? 'positive' : 'neutral'}>{t(proxy.configured ? 'proxies.configured' : 'proxies.not_configured')}</StatusBadge></div>
              <div className="flex flex-wrap gap-sm">
                <Button busy={operation.pending && testing === proxy.id} busyLabel={t('proxies.testing')} disabled={operation.pending || !proxy.configured} onClick={() => {
                  setTesting(proxy.id);
                  void operation.run(async () => apiData(await api.POST('/api/v1/proxies/{proxy_id}/test', { params: { path: { proxy_id: proxy.id } } })), (result) => {
                    setResults((previous) => ({ ...previous, [proxy.id]: result }));
                  });
                }}>{t('proxies.test')}</Button>
                <Button disabled={operation.pending} onClick={() => setEditing(proxy)}>{t('proxies.edit')}</Button>
                <Button variant="plain" disabled={operation.pending} onClick={() => { operation.clearError(); setDeleting(proxy); }}>{t('proxies.delete')}</Button>
              </div></div>
            {result && <InlineMessage tone={result.ok ? 'positive' : 'danger'}>{result.ok ? t('proxies.test_connected')
              : result.reason ? message(result.reason) : t('proxies.test_failed')}</InlineMessage>}
          </li>; })}</ul>}
    {proxies.data && proxies.error && <InlineMessage tone="warning" action={<Button onClick={() => { void proxies.refetch(); }}>{t('common.retry')}</Button>}>{describe(proxies.error)}</InlineMessage>}
    {operation.errorText && <InlineMessage tone="danger">{operation.errorText}</InlineMessage>}
    {proxies.hasNextPage && <Button busy={proxies.isFetchingNextPage} onClick={() => { void proxies.fetchNextPage(); }}>{t('proxies.more')}</Button>}
    <Dialog open={editing !== null} onOpenChange={(value) => { if (!value) setEditing(null); }} title={t(editing === 'new' ? 'proxies.add' : 'proxies.edit')} closeLabel={t('common.close')}>
      {editing !== null && <ProxyForm key={editing === 'new' ? 'new' : editing.id} {...(editing === 'new' ? {} : { proxy: editing })} onSaved={(saved) => {
        setResults((previous) => { const next = { ...previous }; delete next[saved.id]; return next; });
        changed(); setEditing(null);
      }} />}
    </Dialog>
    <ConfirmDialog open={deleting !== null} onOpenChange={(value) => { if (!value && !operation.pending) setDeleting(null); }} title={t('proxies.delete')}
      description={deleting ? t('proxies.delete_effect', { name: deleting.name }) : ''} confirmLabel={t('proxies.delete')} cancelLabel={t('common.cancel')}
      destructive pending={operation.pending} {...(operation.errorText ? { error: operation.errorText } : {})} onConfirm={() => {
        if (deleting) void operation.run(async () => { await api.DELETE('/api/v1/proxies/{proxy_id}', { params: { path: { proxy_id: deleting.id } } }); }, () => {
          changed(); setDeleting(null);
        });
      }} />
  </section>;
}
