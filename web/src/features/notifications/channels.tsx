import { createContext, useContext, useEffect, useId, useMemo, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { Button, Checkbox, ConfirmDialog, Dialog, FormSection, InlineMessage, Skeleton, StatePanel, StatusBadge } from '../../components/ui';
import { apiKey } from '../../api/keys';
import { ApiError } from '../../api/errors';
import { useApi, useApiError, useMessageText, useTimeZone } from '../../api/hooks';
import { useTranslation } from '../../i18n';
import { formatDateTime, formatInteger } from '../../i18n/format';
import { channelCatalog } from './catalog';
import { createChannelAdapter } from './adapter';
import { ChannelEditor } from './channel-editor';
import { useNotificationOperation } from './use-notification-operation';
import type { ChannelAdapter, ChannelRow, ChannelSetupProps, TestReceipt } from './types';

const AdapterContext = createContext<ChannelAdapter | undefined>(undefined);
export function NotificationAdapterProvider({ adapter, children }: { adapter: ChannelAdapter; children: ReactNode }) { return <AdapterContext.Provider value={adapter}>{children}</AdapterContext.Provider>; }
export function useChannelAdapter(supplied?: ChannelAdapter) { const inherited = useContext(AdapterContext); const api = useApi(); const canonical = useMemo(() => createChannelAdapter(api), [api]); return supplied ?? inherited ?? canonical; }
export function useChannels(adapter: ChannelAdapter) {
  return useQuery({ queryKey: apiKey('channels'), queryFn: ({ signal }) => adapter.list(signal) });
}

function DeliveryHistory({ channel, adapter, timeZone }: { channel: ChannelRow; adapter: ChannelAdapter; timeZone: string }) {
  const { t, locale } = useTranslation();
  const errorText = useApiError(); const messageText = useMessageText();
  const history = useQuery({ queryKey: apiKey('deliveries', channel.id), queryFn: ({ signal }) => adapter.history(channel.id, signal) });
  const [expanded, setExpanded] = useState<number | null>(null);
  const details = useQuery({ queryKey: apiKey('deliveries', 'items', expanded), enabled: expanded !== null, queryFn: ({ signal }) => { if (expanded === null) throw new Error('未选择发送记录'); return adapter.deliveryItems(expanded, signal); } });
  function kindLabel(kind: string | null) {
    switch (kind) {
      case 'recommended': return t('web.notify.delivery.recommended'); case 'all_new': return t('web.notify.delivery.all_new');
      case 'price_drop': return t('web.notify.delivery.price_drop'); case 'summary': return t('web.notify.delivery.summary');
      case 'system': return t('web.notify.delivery.system'); case 'test': return t('web.notify.delivery.test'); default: return t('web.notify.unknown');
    }
  }
  return <div className="space-y-group">
    <p className="text-body text-text-secondary">{t('web.notify.accepted')}</p>
    <Button busy={history.isFetching} onClick={() => void history.refetch()}>{t('web.notify.refresh')}</Button>
    {history.isPending && <StatePanel kind="pending" label={t('common.loading')}><Skeleton /><Skeleton /></StatePanel>}
    {history.error && <InlineMessage tone="danger">{errorText(history.error)}</InlineMessage>}
    {history.data?.length === 0 && <StatePanel kind="empty" title={t('web.notify.history_empty')} />}
    <ol className="divide-y divide-border">
      {history.data?.map((delivery) => <li key={delivery.id} className="space-y-sm py-lg">
        <div className="flex flex-wrap items-center justify-between gap-sm"><span className="text-body font-medium">{kindLabel(delivery.kind)}</span><StatusBadge tone={delivery.ok === true ? 'positive' : delivery.ok === false ? 'danger' : 'neutral'}>{delivery.ok === true ? t('web.notify.sent') : delivery.ok === false ? t('web.notify.failed') : t('web.notify.unknown')}</StatusBadge></div>
        <dl className="grid gap-sm text-body sm:grid-cols-2"><div><dt className="text-text-secondary">{t('web.notify.delivery.time')}</dt><dd>{delivery.at === null ? t('web.notify.unknown') : formatDateTime(delivery.at, locale, timeZone) + ' · ' + timeZone}</dd></div><div><dt className="text-text-secondary">{t('web.notify.delivery.task')}</dt><dd>{delivery.task_id === null ? t('web.notify.not_applicable') : t('web.notify.task_reference', { id: delivery.task_id })}</dd></div><div><dt className="text-text-secondary">{t('web.notify.delivery.count')}</dt><dd>{delivery.item_count === null ? t('web.notify.unknown') : formatInteger(delivery.item_count, locale)}</dd></div></dl>
        {delivery.error && <p className="break-words text-body text-danger">{messageText(delivery.error)}</p>}
        {delivery.ok === false && delivery.item_count !== null && delivery.item_count > 0 && <Button aria-expanded={expanded === delivery.id} onClick={() => setExpanded((old) => old === delivery.id ? null : delivery.id)}>{t('web.notify.delivery.view_items')}</Button>}
        {expanded === delivery.id && <div className="space-y-sm rounded-control bg-inset p-md">
          {details.isPending && <StatePanel kind="pending" label={t('common.loading')} />}
          {details.error && <StatePanel kind="error" title={t('web.notify.delivery.items_failed')} description={errorText(details.error)} retry={{ label: t('common.retry'), run: () => void details.refetch() }} />}
          {details.data?.length === 0 && <p className="text-body text-text-secondary">{t('web.notify.history_item_removed')}</p>}
          <ul className="space-y-sm">{details.data?.map((item) => <li key={item.finding_id} className="break-words text-body">{item.title === null ? <span>{t('web.notify.history_item_removed')}</span> : <Link className="inline-flex min-h-control items-center text-accent-text underline underline-offset-4" to="/results" search={{ task_id: item.task_id ?? undefined, finding_id: item.finding_id }}>{item.title}</Link>}</li>)}</ul>
        </div>}
      </li>)}
    </ol>
  </div>;
}

function ChannelCard({ channel, adapter, onEdit, timeZone, editing }: { channel: ChannelRow; adapter: ChannelAdapter; onEdit(): void; timeZone: string; editing: boolean }) {
  const { t, locale } = useTranslation(); const errorText = useApiError(); const messageText = useMessageText(); const queryClient = useQueryClient();
  const [action, setAction] = useState<'test' | 'toggle' | 'remove' | null>(null);
  const [error, setError] = useState<unknown>(null); const [test, setTest] = useState<TestReceipt | null>(null);
  const [unknownTest, setUnknownTest] = useState(false); const [confirmDelete, setConfirmDelete] = useState(false); const [historyOpen, setHistoryOpen] = useState(false);
  const operation = useNotificationOperation();
  const previousTestAt = useRef<number | null>(channel.testedAt);
  const testDescriptionID = useId();
  useEffect(() => {
    if (operation.authenticated === false) { setUnknownTest((old) => old || action === 'test'); setAction(null); setConfirmDelete(false); setHistoryOpen(false); setTest(null); setError(null); }
  }, [operation.authenticated, action]);
  useEffect(() => { if (unknownTest && channel.testedAt !== previousTestAt.current) setUnknownTest(false); }, [channel.testedAt, unknownTest]);
  async function perform(kind: 'test' | 'toggle' | 'remove') {
    if (action || operation.authenticated === false) return;
    const controller = operation.begin(); if (!controller) return; setAction(kind); setError(null);
    if (kind === 'test') { previousTestAt.current = channel.testedAt; setTest(null); setUnknownTest(false); }
    try {
      if (kind === 'test') { const result = await adapter.test(channel.id, controller.signal); if (!operation.isCurrent(controller)) return; setTest(result); }
      if (kind === 'toggle') { await adapter.setEnabled(channel.id, !channel.enabled, controller.signal); if (!operation.isCurrent(controller)) return; }
      if (kind === 'remove') { await adapter.remove(channel.id, controller.signal); if (!operation.isCurrent(controller)) return; setConfirmDelete(false); }
      await queryClient.invalidateQueries({ queryKey: apiKey('channels') });
      if (!operation.isCurrent(controller)) return;
      await queryClient.invalidateQueries({ queryKey: apiKey('deliveries', channel.id) });
      if (!operation.isCurrent(controller)) return;
      if (kind === 'remove') { await queryClient.invalidateQueries({ queryKey: apiKey('settings') }); if (!operation.isCurrent(controller)) return; await queryClient.invalidateQueries({ queryKey: apiKey('tasks') }); }
    } catch (failure) { if (operation.isCurrent(controller)) { setError(failure); if (kind === 'test') setUnknownTest(!(failure instanceof ApiError && [400, 401, 403, 404, 409, 413, 415, 422, 429].includes(failure.status))); } }
    finally { if (operation.isCurrent(controller)) setAction(null); }
  }
  return <li className="space-y-md border-b border-border py-lg last:border-0">
    <header className="flex flex-wrap items-start justify-between gap-md"><div className="min-w-0 space-y-xs"><h3 className="break-words text-section font-semibold">{channel.name}</h3><p className="text-body text-text-secondary">{t(channelCatalog[channel.kind].label)}</p></div><StatusBadge tone={channel.enabled ? 'positive' : 'neutral'}>{channel.enabled ? t('web.notify.enabled') : t('web.notify.disabled')}</StatusBadge></header>
    <p className="text-body text-text-secondary">{channel.testedAt === null ? t('web.notify.not_tested') : t('web.notify.last_test', { time: formatDateTime(channel.testedAt, locale, timeZone), result: channel.testOK === true ? t('web.notify.sent') : channel.testOK === false ? t('web.notify.failed') : t('web.notify.unknown') })}</p>
    {channel.testError && <p className="break-words text-body text-danger">{messageText(channel.testError)}</p>}
    <div className="flex flex-wrap gap-sm"><Button disabled={action !== null || editing} aria-label={t('web.notify.edit_named', { name: channel.name })} onClick={onEdit}>{t('web.notify.edit')}</Button><Button busy={action === 'toggle'} disabled={action !== null || editing} onClick={() => void perform('toggle')}>{channel.enabled ? t('web.notify.disable') : t('web.notify.enable')}</Button><Button busy={action === 'test'} disabled={action !== null || unknownTest || editing} aria-label={t('web.notify.test_named', { name: channel.name })} aria-describedby={testDescriptionID} onClick={() => void perform('test')}>{t('web.notify.send_test')}</Button><Button onClick={() => { setHistoryOpen(true); }}>{t('web.notify.history_recent')}</Button><Button variant="destructive" disabled={action !== null || editing} onClick={() => { setError(null); setConfirmDelete(true); }}>{t('web.notify.delete')}</Button></div>
    <p id={testDescriptionID} className="text-caption text-text-secondary">{t('web.notify.test_fixed_sample')}</p>
    <p className="text-caption text-text-secondary">{t('web.notify.test_eligibility')}</p>
    {error !== null && <InlineMessage tone="danger"><p>{errorText(error)}</p>{error instanceof ApiError && error.requestID && <p className="break-all">{t('web.request_id', { request_id: error.requestID })}</p>}</InlineMessage>}
    {unknownTest && <InlineMessage tone="warning">{t('web.notify.test_not_confirmed')}</InlineMessage>}
    {test && <InlineMessage tone={test.ok ? 'positive' : 'danger'}><p>{test.ok ? t('web.notify.sent') : test.error ? messageText(test.error) : t('web.notify.failed')}</p>{test.ok && <p>{t('web.notify.accepted')}</p>}</InlineMessage>}
    <ConfirmDialog open={confirmDelete} onOpenChange={(open) => { if (action !== 'remove') setConfirmDelete(open); }} title={t('web.notify.delete_named', { name: channel.name })} description={t('web.notify.delete_help')} confirmLabel={t('web.notify.delete')} cancelLabel={t('common.cancel')} destructive pending={action === 'remove'} {...(error !== null ? { error: errorText(error) } : {})} onConfirm={() => void perform('remove')} />
    <Dialog open={historyOpen} onOpenChange={setHistoryOpen} title={t('web.notify.history_named', { name: channel.name })} closeLabel={t('common.close')}><DeliveryHistory channel={channel} adapter={adapter} timeZone={timeZone} /></Dialog>
  </li>;
}

export function ChannelSetup({ adapter: supplied, onSaved, onAvailabilityChange, timeZone }: ChannelSetupProps & { timeZone?: string | undefined }) {
  const adapter = useChannelAdapter(supplied); const query = useChannels(adapter); const queryClient = useQueryClient();
  const configuredTimeZone = useTimeZone(); const displayTimeZone = timeZone ?? configuredTimeZone ?? 'UTC';
  const { t } = useTranslation(); const errorText = useApiError();
  const [editing, setEditing] = useState<ChannelRow | 'new' | null>(null); const [savedNotice, setSavedNotice] = useState(false);
  const addButton = useRef<HTMLButtonElement>(null);
  const [savedVersions, setSavedVersions] = useState<Record<number, number>>({});
  const [loadingEdit, setLoadingEdit] = useState(false); const [editError, setEditError] = useState<unknown>(null);
  const editOperation = useNotificationOperation();
  useEffect(() => { if (editOperation.authenticated === false) { setLoadingEdit(false); setEditError(null); setSavedNotice(false); } }, [editOperation.authenticated]);
  async function edit(id: number) {
    if (editing !== null || loadingEdit || editOperation.authenticated === false) return;
    const controller = editOperation.begin(); if (!controller) return; setLoadingEdit(true); setEditError(null);
    try { const channel = await adapter.get(id, controller.signal); if (editOperation.isCurrent(controller)) setEditing(channel); } catch (failure) { if (editOperation.isCurrent(controller)) setEditError(failure); }
    finally { if (editOperation.isCurrent(controller)) setLoadingEdit(false); }
  }
  useEffect(() => { onAvailabilityChange?.(query.data ? query.data.length > 0 : null); }, [adapter, query.data, onAvailabilityChange]);
  function saved(channel: ChannelRow) {
    queryClient.setQueryData<ChannelRow[]>(apiKey('channels'), (old) => [...(old ?? []).filter((value) => value.id !== channel.id), channel]);
    void queryClient.invalidateQueries({ queryKey: apiKey('channels') }); setSavedVersions((old) => ({ ...old, [channel.id]: (old[channel.id] ?? 0) + 1 })); setEditing(null); setSavedNotice(true); onSaved?.(channel.id); addButton.current?.focus();
  }
  return <FormSection title={t('web.notify.channels_title')} description={t('web.notify.channels_help')} actions={<Button ref={addButton} onClick={() => { setEditing('new'); setSavedNotice(false); }} disabled={editing !== null || loadingEdit}>{t('web.notify.add_channel')}</Button>}>
    <div className="space-y-group">
      {query.isPending && <StatePanel kind="pending" label={t('common.loading')}><Skeleton /><Skeleton /></StatePanel>}
      {query.error && <StatePanel kind="error" title={t('web.notify.channels_failed')} description={errorText(query.error)} retry={{ label: t('common.retry'), run: () => void query.refetch() }} />}
      {editError !== null && <InlineMessage tone="danger">{errorText(editError)}</InlineMessage>}
      {loadingEdit && <StatePanel kind="pending" label={t('common.loading')}><Skeleton /></StatePanel>}
      {savedNotice && <InlineMessage tone="positive">{t('web.notify.saved_no_test')}</InlineMessage>}
      {editing !== null && <ChannelEditor key={editing === 'new' ? 'new' : editing.id} channel={editing === 'new' ? null : editing} adapter={adapter} onSaved={saved} onCancel={() => { setEditing(null); addButton.current?.focus(); }} />}
      {query.data?.length === 0 && editing === null && <StatePanel kind="empty" title={t('web.notify.channels_empty')} description={t('web.notify.channels_empty_help')} />}
      {query.data && query.data.length > 0 && !query.data.some((channel) => channel.enabled) && <InlineMessage tone="warning">{t('web.notify.no_enabled_channels')}</InlineMessage>}
      <ul>{query.data?.map((channel) => <ChannelCard key={channel.id + '-' + (savedVersions[channel.id] ?? 0)} channel={channel} adapter={adapter} timeZone={displayTimeZone} editing={editing !== null || loadingEdit} onEdit={() => void edit(channel.id)} />)}</ul>
      <Button busy={query.isFetching} onClick={() => void query.refetch()}>{t('web.notify.refresh')}</Button>
    </div>
  </FormSection>;
}

export function TaskChannelPicker({ value, onChange, disabled = false, error, adapter: supplied }: { value: number[] | undefined; onChange(ids: number[]): void; disabled?: boolean; error?: string | undefined; adapter?: ChannelAdapter | undefined }) {
  const adapter = useChannelAdapter(supplied); const query = useChannels(adapter); const { t } = useTranslation(); const errorText = useApiError();
  const selected = value ?? query.data?.filter((channel) => channel.enabled).map((channel) => channel.id) ?? [];
  return <div className="space-y-sm"><fieldset className="space-y-sm" disabled={disabled || !query.data}><legend className="text-body font-medium">{t('web.notify.task_targets')}</legend>
    {query.isPending && <p role="status">{t('common.loading')}</p>}
    {query.error && <p role="alert" className="text-body text-danger">{errorText(query.error)}</p>}
    {query.data?.map((channel) => <Checkbox key={channel.id} label={channel.name} hint={t(channelCatalog[channel.kind].label) + ' · ' + (channel.enabled ? t('web.notify.enabled') : t('web.notify.disabled'))} checked={selected.includes(channel.id)} onCheckedChange={(checked) => onChange(checked ? [...selected, channel.id] : selected.filter((id) => id !== channel.id))} />)}
    {selected.filter((id) => !query.data?.some((channel) => channel.id === id)).map((id) => query.data ? <Checkbox key={id} label={t('web.notify.unknown_target', { id })} checked onCheckedChange={(checked) => { if (!checked) onChange(selected.filter((value) => value !== id)); }} /> : <p key={id} className="text-body text-text-secondary">{t('web.notify.unknown_target', { id })}</p>)}
    {value !== undefined && selected.length === 0 && <p className="text-body text-text-secondary">{t('web.notify.task_empty_channels')}</p>}
    {error && <p role="alert" className="text-body text-danger">{error}</p>}
  </fieldset>{query.error && <Button disabled={disabled} onClick={() => void query.refetch()}>{t('common.retry')}</Button>}</div>;
}
