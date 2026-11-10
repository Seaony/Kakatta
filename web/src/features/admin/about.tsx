import { useState } from 'react';
import { Link } from '@tanstack/react-router';
import { useQuery, useQueryClient, type UseQueryResult } from '@tanstack/react-query';
import { apiData, useApi, useMessageText } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import type { components } from '../../api/schema.gen';
import { Button, CopyButton, FormSection, InlineMessage, PageHeader, StatePanel, Switch, TextField } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { Command, ErrorNotice, useAdminSettings, useAdminTime, usePrivateAction } from './shared';
import { kakattaLicense } from './license';

type Json = components['schemas']['JsonValue'];
type Document = components['schemas']['OpenAPIDocument'];
type UpdateStatus = components['schemas']['UpdateStatus'];
type OperationView = { method: string; path: string; summary: string; request: Json | undefined; responses: Json | undefined; security: Json | undefined };

function record(value: Json | undefined): value is { [key: string]: Json } {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

export function documentOperations(document: Document): OperationView[] {
  const paths = document.paths;
  if (!record(paths)) return [];
  return Object.entries(paths).flatMap(([path, item]) => {
    if (!record(item)) return [];
    return ['get', 'post', 'put', 'patch', 'delete', 'head', 'options', 'trace'].flatMap((method) => {
      const operation = item[method];
      if (!record(operation)) return [];
      const request = operation.requestBody !== undefined || operation.parameters !== undefined ? {
        ...(operation.parameters !== undefined ? { parameters: operation.parameters } : {}),
        ...(operation.requestBody !== undefined ? { requestBody: operation.requestBody } : {}),
      } : undefined;
      return [{ method: method.toUpperCase(), path, summary: typeof operation.summary === 'string' ? operation.summary : '', request, responses: operation.responses, security: operation.security ?? document.security }];
    });
  });
}

function OpenAPIReader() {
  const { t } = useTranslation();
  const api = useApi();
  const [opened, setOpened] = useState(false);
  const [search, setSearch] = useState('');
  const document = useQuery({ queryKey: apiKey('about', 'openapi'), enabled: opened, queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/openapi.json', { signal })) });
  const operations = document.data ? documentOperations(document.data) : [];
  const filtered = operations.filter((operation) => `${operation.method} ${operation.path} ${operation.summary}`.toLowerCase().includes(search.trim().toLowerCase()));
  return <FormSection title={t('admin.about.api_title')} description={t('admin.about.api_description')} actions={<Button onClick={() => setOpened(!opened)}>{t(opened ? 'admin.about.hide_docs' : 'admin.about.show_docs')}</Button>}>
    {opened && <div className="min-w-0 space-y-lg">
      {document.isPending && <StatePanel kind="pending" label={t('common.loading')} />}
      <ErrorNotice error={document.error} />{document.isError && <Button onClick={() => void document.refetch()}>{t('common.retry')}</Button>}
      {document.data && <>
        <TextField label={t('admin.about.api_search')} value={search} onChange={(event) => setSearch(event.target.value)} type="search" />
        <a href="/api/v1/openapi.json" download="kakatta-openapi.json" className="inline-flex min-h-control items-center text-body text-accent-text underline">{t('admin.about.api_download')}</a>
        {!filtered.length && <StatePanel kind="empty" title={t('admin.about.api_empty')} action={<Button onClick={() => setSearch('')}>{t('admin.about.clear_search')}</Button>} />}
        <div className="divide-y divide-border">{filtered.map((operation) => <details key={`${operation.method} ${operation.path}`} className="min-w-0 py-md"><summary className="min-h-control cursor-pointer space-x-sm break-all py-sm text-body"><strong className="font-semibold">{operation.method}</strong><code>{operation.path}</code>{operation.summary && <span className="block break-words text-text-secondary">{operation.summary}</span>}</summary><div className="min-w-0 space-y-lg py-md">{([
          ['admin.about.authentication', operation.security], ['admin.about.request', operation.request], ['admin.about.response', operation.responses],
        ] as const).map(([key, value]) => <div key={key} className="min-w-0 space-y-sm"><h3 className="text-body font-medium">{t(key)}</h3><pre className="max-w-full overflow-x-auto rounded-control bg-inset p-md text-caption"><code>{value === undefined ? t('admin.about.none') : JSON.stringify(value, null, 2)}</code></pre></div>)}</div></details>)}</div>
        {record(document.data.components) && <details className="min-w-0"><summary className="min-h-control cursor-pointer py-sm text-body font-medium">{t('admin.about.schemas')}</summary><pre className="max-h-[32rem] max-w-full overflow-auto rounded-control bg-inset p-md text-caption"><code>{JSON.stringify(document.data.components.schemas ?? {}, null, 2)}</code></pre></details>}
      </>}
    </div>}
  </FormSection>;
}

function UpdateSettings({ update }: { update: UseQueryResult<UpdateStatus> }) {
  const { t } = useTranslation();
  const api = useApi();
  const client = useQueryClient();
  const message = useMessageText();
  const time = useAdminTime();
  const settings = useAdminSettings();
  const action = usePrivateAction();
  const [draft, setDraft] = useState<boolean | null>(null);
  const [saved, setSaved] = useState(false);
  const value = settings.data?.values['general.update_check'];
  const info = update.data;
  const release = info?.known_release;
  const enabled = typeof value === 'boolean' ? value : info?.enabled;
  const fresh = !update.isError && info && !info.stale && info.enabled && enabled;
  const available = fresh && info.update_available === true && release;
  const imageTag = available ? /^v((?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*))$/.exec(available.version)?.[1] : undefined;
  return <FormSection title={t('admin.about.updates')} description={t('admin.about.update_description')}>
    {settings.isPending ? <StatePanel kind="pending" label={t('common.loading')} /> : settings.data && typeof value === 'boolean' ? <form className="space-y-md" onSubmit={(event) => {
      event.preventDefault();
      void action.run(async (signal) => apiData(await api.PATCH('/api/v1/settings', { signal, body: { values: { 'general.update_check': draft ?? value } } })), (next) => {
        client.setQueryData(apiKey('settings'), next);
        void client.invalidateQueries({ queryKey: apiKey('update-status') });
        setDraft(null);
        setSaved(true);
      });
    }}><Switch label={t('admin.about.check_enabled')} hint={t('admin.about.check_hint')} checked={draft ?? value} onCheckedChange={(next) => { setDraft(next); setSaved(false); }} disabled={action.pending} /><Button variant="primary" type="submit" busy={action.pending} disabled={draft === null}>{t('admin.save')}</Button><ErrorNotice error={action.error} />{saved && <p role="status" className="text-body text-positive">{t('admin.saved')}</p>}</form> : <ErrorNotice error={settings.error} />}
    {update.isPending && <StatePanel kind="pending" label={t('common.loading')} />}
    {update.isError && <><ErrorNotice error={update.error} />{info && <InlineMessage tone="warning">{t('admin.stale')}</InlineMessage>}<Button onClick={() => void update.refetch()} busy={update.isFetching}>{t('common.retry')}</Button></>}
    {info && <div className="space-y-md">
      {enabled === false ? <InlineMessage tone="neutral">{t('admin.about.update_disabled')}</InlineMessage>
        : available ? <InlineMessage tone="positive">{t('admin.about.update_ready', { version: available.version })}</InlineMessage>
        : fresh && info.update_available === false ? <InlineMessage tone="neutral">{t('admin.about.update_current')}</InlineMessage>
        : <InlineMessage tone={info.stale && release ? 'warning' : 'info'}>{t(info.stale && release ? 'admin.about.update_stale' : 'admin.about.update_unknown')}</InlineMessage>}
      {info.error && <InlineMessage tone="warning">{message(info.error)}</InlineMessage>}
      <dl className="grid min-w-0 gap-lg text-body sm:grid-cols-2">
        <div><dt className="text-text-secondary">{t('admin.about.release_version')}</dt><dd>{release?.version ?? t('admin.unknown')}</dd></div>
        <div><dt className="text-text-secondary">{t('admin.about.last_attempt')}</dt><dd className="tabular-nums">{time(info.last_attempt_at)}</dd></div>
        <div><dt className="text-text-secondary">{t('admin.about.last_success')}</dt><dd className="tabular-nums">{time(info.last_success_at)}</dd></div>
        {enabled !== false && <div><dt className="text-text-secondary">{t('admin.about.next_check')}</dt><dd className="tabular-nums">{time(info.next_check_at)}</dd></div>}
      </dl>
      {release && <a href={release.url} target="_blank" rel="noreferrer" className="inline-flex min-h-control items-center text-body text-accent-text underline">{t('admin.about.release_notes')}</a>}
    </div>}
    <p className="max-w-prose text-body text-text-secondary">{t('admin.about.manual_update')}</p>
    <div className="space-y-lg">
      {imageTag && <Command label={t('admin.about.pin_image')} command={`image: ghcr.io/seaony/kakatta:${imageTag}`} />}
      <Command label={t('admin.about.pull')} command="docker compose pull kakatta" />
      <Command label={t('admin.about.recreate')} command="docker compose up -d kakatta" />
    </div>
    <p className="max-w-prose text-body text-text-secondary">{t('admin.about.image_required')}</p>
    <p className="max-w-prose text-body text-text-secondary">{t('admin.about.rollback')}</p>
    <Link to="/settings/backup" className="inline-flex min-h-control items-center text-body text-accent-text underline">{t('admin.backup.title')}</Link>
  </FormSection>;
}

export function AboutPage() {
  const { t } = useTranslation();
  const api = useApi();
  const update = useQuery({ queryKey: apiKey('update-status'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/system/update', { signal })) });
  const mcp = new URL('/mcp', globalThis.location.origin).href;
  return <div className="min-w-0 space-y-xxl">
    <PageHeader title={t('admin.about.title')} description={t('admin.about.description')} />
    <FormSection title={t('admin.about.version')}><p className="break-all text-body tabular-nums">{update.data?.current_version ?? t(update.isPending ? 'common.loading' : 'admin.unknown')}</p><a className="inline-flex min-h-control items-center text-body text-accent-text underline" href="https://github.com/Seaony/Kakatta" target="_blank" rel="noreferrer">{t('admin.about.repository')}</a></FormSection>
    <UpdateSettings update={update} />
    <FormSection title={t('admin.about.disclaimer')}>
      <p className="max-w-prose text-body">{t('admin.about.account_risk')}</p><p className="max-w-prose text-body text-text-secondary">{t('admin.about.read_only')}</p><p className="max-w-prose text-body text-text-secondary">{t('admin.about.purchase_reference')}</p>
    </FormSection>
    <FormSection title={t('admin.about.licenses')}>
      <p className="max-w-prose text-body">{t('admin.about.license_summary')}</p>
      <details><summary className="min-h-control cursor-pointer py-sm text-body font-medium">{t('admin.about.license_kakatta')}</summary><pre className="max-w-prose whitespace-pre-wrap break-words py-md text-body" lang="en">{kakattaLicense}</pre></details>
      <div className="flex flex-wrap gap-lg"><a href="https://github.com/usagi-org/ai-goofish-monitor" target="_blank" rel="noreferrer" className="inline-flex min-h-control items-center text-body text-accent-text underline">{t('admin.about.license_upstream')}</a><a href="https://github.com/Seaony/Kakatta/tree/master/internal/remote/assets/novnc" target="_blank" rel="noreferrer" className="inline-flex min-h-control items-center text-body text-accent-text underline">{t('admin.about.license_remote')}</a></div>
      <p className="max-w-prose text-body text-text-secondary">{t('admin.about.license_pending')}</p>
    </FormSection>
    <OpenAPIReader />
    <FormSection title={t('admin.about.mcp')} description={t('admin.about.mcp_description')}>
      <TextField label={t('admin.about.mcp_address')} value={mcp} readOnly /><CopyButton text={mcp} label={t('admin.copy')} copiedLabel={t('admin.copied')} errorLabel={t('admin.copy_failed')} />
      <pre className="max-w-full overflow-x-auto rounded-control bg-inset p-md text-body"><code>Authorization: Bearer &lt;{t('admin.about.token_placeholder')}&gt;</code></pre>
      <p className="max-w-prose text-body text-text-secondary">{t('admin.about.mcp_tools')}</p>
      <Link to="/settings/security" className="inline-flex min-h-control items-center text-body text-accent-text underline">{t('admin.tokens.create')}</Link>
    </FormSection>
  </div>;
}
