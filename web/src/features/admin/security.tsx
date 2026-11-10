import { useEffect, useRef, useState } from 'react';
import { Link } from '@tanstack/react-router';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { apiData, useApi, useApiError, useMessageText } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import { useAuthentication } from '../../app/auth-state';
import type { components } from '../../api/schema.gen';
import { ApiError } from '../../api/errors';
import { Button, ConfirmDialog, CopyButton, Dialog, FormSection, InlineMessage, PageHeader, StatePanel, StatusBadge, TextAreaField, TextField } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { Command, ErrorNotice, fieldError, useAdminSettings, useAdminTime, usePrivateAction } from './shared';

type Token = components['schemas']['TokenMetadata'];
type Binding = components['schemas']['TOTPSetup'];
type Action = 'password' | 'setup' | 'disable' | 'create' | 'issued' | null;

function QRCode({ uri }: { uri: string }) {
  const { t } = useTranslation();
  const [image, setImage] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let cancelled = false;
    setImage(null);
    setFailed(false);
    void import('qrcode').then((qr) => qr.toString(uri, { type: 'svg', errorCorrectionLevel: 'M', margin: 4, width: 256 }))
      .then((value) => { if (!cancelled) setImage(`data:image/svg+xml;charset=utf-8,${encodeURIComponent(value)}`); })
      .catch(() => { if (!cancelled) setFailed(true); });
    return () => { cancelled = true; };
  }, [uri]);
  return <div className="space-y-sm">{image ? <img src={image} alt={t('admin.totp.qr')} width={256} height={256} className="max-w-full rounded-control" /> : <p role="status" className="text-body">{t(failed ? 'admin.totp.qr_failed' : 'common.loading')}</p>}</div>;
}

function Credentials({ kind, tokens, revoke }: { kind: Token['kind']; tokens: Token[]; revoke(token: Token): void }) {
  const { t } = useTranslation();
  const time = useAdminTime();
  const rows = tokens.filter((token) => token.kind === kind);
  if (!rows.length) return <StatePanel kind="empty" title={t(kind === 'web_session' ? 'admin.devices.empty' : 'admin.tokens.empty')} />;
  return <ul className="divide-y divide-border">{rows.map((token) => {
    const revoked = token.revoked_at !== null;
    const expired = !revoked && token.expires_at !== null && token.expires_at <= Date.now();
    return <li key={token.id} className="min-w-0 space-y-md py-lg first:pt-0 last:pb-0"><div className="flex flex-wrap items-start justify-between gap-md"><div className="min-w-0 space-y-sm"><p className="break-words text-body font-medium">{token.name}</p><div className="flex flex-wrap gap-sm">{token.current && <StatusBadge tone="accent">{t('admin.current')}</StatusBadge>}<StatusBadge tone={revoked || expired ? 'neutral' : 'positive'}>{t(revoked ? 'admin.revoked' : expired ? 'admin.expired' : 'admin.active')}</StatusBadge></div></div>{!revoked && !expired && <Button variant="destructive" onClick={() => revoke(token)} aria-label={t('admin.revoke.title', { name: token.name })}>{t('admin.revoke')}</Button>}</div><dl className="grid min-w-0 gap-md text-body sm:grid-cols-3"><div><dt className="text-text-secondary">{t('admin.created')}</dt><dd className="break-words tabular-nums">{time(token.created_at)}</dd></div><div><dt className="text-text-secondary">{t('admin.last_used')}</dt><dd className="break-words tabular-nums">{token.last_used_at === null ? t('admin.tokens.never_used') : time(token.last_used_at)}</dd></div><div><dt className="text-text-secondary">{t('admin.expires')}</dt><dd className="break-words tabular-nums">{kind === 'api_token' && token.expires_at === null ? t('admin.never') : time(token.expires_at)}</dd></div></dl></li>;
  })}</ul>;
}

function TrustedProxies() {
  const { t } = useTranslation();
  const message = useMessageText();
  const api = useApi();
  const settings = useAdminSettings();
  const client = useQueryClient();
  const action = usePrivateAction();
  const [draft, setDraft] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const definitions = useQuery({ queryKey: apiKey('setting-definitions'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings/definitions', { signal })) });
  const definition = definitions.data?.find((entry) => entry.key === 'security.trusted_proxies');
  const value = settings.data?.values['security.trusted_proxies'];
  const stored = Array.isArray(value) ? value.filter((entry): entry is string => typeof entry === 'string').join('\n') : '';
  const environment = settings.data?.sources['security.trusted_proxies'] === 'environment';
  return <FormSection title={t('admin.proxy.title')} description={definition ? message(definition.why) : undefined}>
    {settings.isPending ? <StatePanel kind="pending" label={t('common.loading')} /> : settings.isError && !settings.data ? <ErrorNotice error={settings.error} /> : <form className="max-w-prose space-y-md" onSubmit={(event) => {
      event.preventDefault();
      if (environment) return;
      const entries = (draft ?? stored).split('\n').map((entry) => entry.trim()).filter(Boolean);
      void action.run(async (signal) => apiData(await api.PATCH('/api/v1/settings', { signal, body: { values: { 'security.trusted_proxies': entries } } })), (next) => {
        client.setQueryData(apiKey('settings'), next);
        setDraft(null);
        setSaved(true);
      });
    }}>
      <TextAreaField label={t('admin.proxy.label')} value={draft ?? stored} onChange={(event) => { setDraft(event.target.value); setSaved(false); }} disabled={environment || action.pending} {...fieldError(action.field('security.trusted_proxies'))} />
      {environment && <InlineMessage tone="info">{t('admin.proxy.environment')}</InlineMessage>}
      {definition && <p className="text-body text-text-secondary">{message(definition.risk)}</p>}
      {settings.data?.warnings.filter((entry) => entry.key === 'security.trusted_proxies').map((warning) => <InlineMessage key={warning.key} tone="warning">{message(warning.reason)}</InlineMessage>)}
      <ErrorNotice error={action.error} />{saved && <p role="status" className="text-body text-positive">{t('admin.saved')}</p>}
      {!environment && <Button type="submit" variant="primary" busy={action.pending} disabled={draft === null}>{t('admin.save')}</Button>}
    </form>}
  </FormSection>;
}

export function SecurityPage() {
  const { t } = useTranslation();
  const api = useApi();
  const client = useQueryClient();
  const errorText = useApiError();
  const time = useAdminTime();
  const authentication = useAuthentication();
  const me = useQuery({ queryKey: apiKey('me'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/me', { signal })) });
  const tokens = useQuery({ queryKey: apiKey('security-tokens'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/security/tokens', { signal })) });
  const action = usePrivateAction();
  const revocation = usePrivateAction();
  const [mode, setMode] = useState<Action>(null);
  const [password, setPassword] = useState('');
  const [nextPassword, setNextPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [name, setName] = useState('');
  const [code, setCode] = useState('');
  const [binding, setBinding] = useState<Binding | null>(null);
  const [expired, setExpired] = useState(false);
  const [issued, setIssued] = useState('');
  const [validation, setValidation] = useState<string | null>(null);
  const [notice, setNotice] = useState('');
  const [revoking, setRevoking] = useState<Token | null>(null);
  const [signedOut, setSignedOut] = useState(false);
  const form = useRef<HTMLFormElement>(null);
  useEffect(() => {
    if (action.error || validation) form.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
  }, [action.error, validation]);
  useEffect(() => {
    if (authentication?.authenticated === false) {
      clear(); setMode(null); revocation.cancel(); setRevoking(null);
    } else if (authentication?.authenticated) setSignedOut(false);
  }, [authentication?.authenticated]);
  useEffect(() => {
    if (action.error instanceof ApiError && action.error.reason.code === 'auth.challenge_expired') {
      setBinding(null); setCode(''); setExpired(true);
    }
    if (action.error instanceof ApiError && (action.error.reason.code === 'auth.totp_enabled' || action.error.reason.code === 'auth.totp_disabled')) {
      void client.invalidateQueries({ queryKey: apiKey('me') });
    }
  }, [action.error, client]);
  useEffect(() => {
    if (!binding) return;
    const expire = () => { setExpired(true); setBinding(null); setCode(''); };
    const remaining = binding.expires_at - Date.now();
    if (remaining <= 0) { expire(); return; }
    const timer = setTimeout(expire, remaining);
    return () => clearTimeout(timer);
  }, [binding]);
  const refresh = () => {
    void client.invalidateQueries({ queryKey: apiKey('me') });
    void client.invalidateQueries({ queryKey: apiKey('security-tokens') });
  };
  const clear = () => {
    action.cancel();
    setPassword(''); setNextPassword(''); setConfirm(''); setName(''); setCode(''); setBinding(null); setIssued(''); setValidation(null); setExpired(false);
  };
  const open = (value: Action) => { clear(); setMode(value); setNotice(''); };
  const close = () => {
    if (action.pending) { setNotice(t('admin.action_unknown')); refresh(); }
    clear(); setMode(null);
  };
  function submit() {
    setValidation(null);
    if (mode === 'password') {
      if (nextPassword !== confirm) { setValidation(t('admin.password.mismatch')); return; }
      void action.run(async (signal) => { await api.POST('/api/v1/security/password', { signal, body: { old_password: password, new_password: nextPassword } }); }, () => { clear(); setMode(null); setNotice(t('admin.password.changed')); refresh(); });
    } else if (mode === 'create') {
      if (!name.trim()) { setValidation(t('admin.required')); return; }
      void action.run(async (signal) => apiData(await api.POST('/api/v1/security/tokens', { signal, body: { name: name.trim() } })), (value) => { setName(''); setIssued(value.token); setMode('issued'); refresh(); });
    } else if (mode === 'setup' && !binding) {
      void action.run(async (signal) => apiData(await api.POST('/api/v1/security/totp/setup', { signal, body: { password } })), (value) => { setPassword(''); setExpired(false); setBinding(value); });
    } else if (mode === 'setup' && binding) {
      if (!/^[0-9]{6}$/.test(code)) { setValidation(t('admin.totp.code_invalid')); return; }
      void action.run(async (signal) => { await api.POST('/api/v1/security/totp/enable', { signal, body: { setup_token: binding.setup_token, code } }); }, () => { clear(); setMode(null); setNotice(t('admin.totp.changed')); refresh(); });
    } else if (mode === 'disable') {
      if (!/^[0-9]{6}$/.test(code)) { setValidation(t('admin.totp.code_invalid')); return; }
      void action.run(async (signal) => { await api.POST('/api/v1/security/totp/disable', { signal, body: { password, code } }); }, () => { clear(); setMode(null); setNotice(t('admin.totp.changed')); refresh(); });
    }
  }
  const title = mode === 'password' ? t('admin.password.title') : mode === 'disable' ? t('admin.totp.disable') : mode === 'create' || mode === 'issued' ? t('admin.tokens.create') : t('admin.totp.start');
  const revokeError = revocation.error ? [errorText(revocation.error), revocation.error instanceof ApiError && revocation.error.requestID ? t('web.request_id', { request_id: revocation.error.requestID }) : ''].filter(Boolean).join(' ') : undefined;
  if (signedOut) return <StatePanel kind="empty" title={t('admin.revoked.success')} action={<Link to="/login" className="inline-flex min-h-control items-center text-accent-text underline">{t('admin.sign_in')}</Link>} />;
  return <div className="min-w-0 space-y-xxl">
    <PageHeader title={t('admin.security.title')} description={t('admin.security.description')} actions={<Button onClick={refresh} busy={me.isFetching || tokens.isFetching}>{t('admin.refresh')}</Button>} />
    {notice && <InlineMessage tone="info">{notice}</InlineMessage>}
    {me.isError && <ErrorNotice error={me.error} />}
    <FormSection title={t('admin.password.title')} description={t('admin.password.effect')} actions={<Button disabled={!me.data} onClick={() => open('password')}>{t('admin.password.title')}</Button>}>
      {me.data ? <p className="break-words text-body">{t('admin.username')}: {me.data.username}</p> : me.isPending ? <StatePanel kind="pending" label={t('common.loading')} /> : <Button onClick={() => void me.refetch()}>{t('common.retry')}</Button>}
    </FormSection>
    <FormSection title={t('admin.totp.title')} description={t('admin.totp.no_codes')} actions={<Button disabled={!me.data} onClick={() => open(me.data?.totp_enabled ? 'disable' : 'setup')}>{t(me.data?.totp_enabled ? 'admin.totp.disable' : 'admin.totp.start')}</Button>}>
      {me.data && <StatusBadge tone={me.data.totp_enabled ? 'positive' : 'neutral'}>{t(me.data.totp_enabled ? 'admin.enabled' : 'admin.disabled')}</StatusBadge>}
    </FormSection>
    {tokens.isError && <><ErrorNotice error={tokens.error} />{tokens.data && <InlineMessage tone="warning">{t('admin.stale')}</InlineMessage>}</>}
    <FormSection title={t('admin.devices')}>{tokens.data ? <Credentials kind="web_session" tokens={tokens.data.tokens} revoke={setRevoking} /> : tokens.isPending ? <StatePanel kind="pending" label={t('common.loading')} /> : <Button onClick={() => void tokens.refetch()}>{t('common.retry')}</Button>}</FormSection>
    <FormSection title={t('admin.tokens')} description={t('admin.tokens.access')} actions={<Button variant="primary" disabled={!me.data} onClick={() => open('create')}>{t('admin.tokens.create')}</Button>}>
      {tokens.data && <Credentials kind="api_token" tokens={tokens.data.tokens} revoke={setRevoking} />}
    </FormSection>
    <TrustedProxies />
    <FormSection title={t('admin.recovery.title')} description={t('admin.recovery.description')}>
      <div className="space-y-lg"><Command label={t('admin.recovery.password')} command="docker compose exec kakatta kakatta reset-password --data /data" /><Command label={t('admin.recovery.both')} command="docker compose exec kakatta kakatta reset-password --data /data --disable-totp" /><Command label={t('admin.recovery.totp')} command="docker compose exec kakatta kakatta disable-totp --data /data" /></div>
    </FormSection>
    <Dialog open={mode !== null} onOpenChange={(value) => { if (!value) close(); }} title={title} closeLabel={t('common.close')}>
      {mode === 'issued' ? <div className="space-y-lg"><InlineMessage tone="warning">{t('admin.tokens.once')}</InlineMessage><TextAreaField label={t('admin.tokens')} value={issued} readOnly spellCheck={false} autoComplete="off" /><CopyButton text={issued} label={t('admin.copy')} copiedLabel={t('admin.copied')} errorLabel={t('admin.copy_failed')} /><Button onClick={close}>{t('common.close')}</Button></div> : <form ref={form} className="space-y-lg" onKeyDown={(event) => { if (event.key === 'Enter' && event.nativeEvent.isComposing) event.preventDefault(); }} onSubmit={(event) => { event.preventDefault(); submit(); }}>
        {mode === 'password' && <><p className="text-body text-text-secondary">{t('admin.password.effect')}</p><TextField type="password" label={t('admin.password.current')} autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} required disabled={action.pending} {...fieldError(action.field('old_password'))} /><TextField type="password" label={t('admin.password.new')} autoComplete="new-password" value={nextPassword} onChange={(event) => setNextPassword(event.target.value)} required disabled={action.pending} {...fieldError(action.field('new_password'))} /><TextField type="password" label={t('admin.password.confirm')} autoComplete="new-password" value={confirm} onChange={(event) => setConfirm(event.target.value)} required disabled={action.pending} {...fieldError(validation ?? undefined)} /></>}
        {mode === 'create' && <><TextField label={t('admin.tokens.name')} value={name} onChange={(event) => setName(event.target.value)} required disabled={action.pending} {...fieldError(validation ?? action.field('name'))} /><p className="text-body text-text-secondary">{t('admin.tokens.access')}</p><p className="text-body text-text-secondary">{t('admin.tokens.lost')}</p></>}
        {(mode === 'disable' || mode === 'setup' && !binding) && <TextField type="password" label={t('admin.password.current')} autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} required disabled={action.pending} {...fieldError(action.field('password'))} />}
        {mode === 'setup' && expired && <InlineMessage tone="warning">{t('admin.totp.restart')}</InlineMessage>}
        {mode === 'setup' && binding && <><p className="text-body">{t('admin.totp.pending')}</p><QRCode uri={binding.uri} /><TextField label={t('admin.totp.manual')} value={binding.secret} readOnly autoComplete="off" /><CopyButton text={binding.secret} label={t('admin.copy')} copiedLabel={t('admin.copied')} errorLabel={t('admin.copy_failed')} /><p className="text-body text-text-secondary">{t('admin.totp.expires', { time: time(binding.expires_at) })}</p></>}
        {(mode === 'disable' || mode === 'setup' && binding) && <TextField label={t('admin.totp.code')} inputMode="numeric" autoComplete="one-time-code" pattern="[0-9]{6}" maxLength={6} value={code} onChange={(event) => setCode(event.target.value)} required disabled={action.pending} {...fieldError(validation ?? action.field('code'))} />}
        <ErrorNotice error={action.error} />
        <div className="flex flex-wrap gap-sm"><Button type="submit" variant={mode === 'disable' ? 'destructive' : 'primary'} busy={action.pending} disabled={action.blocked}>{mode === 'setup' ? t(binding ? 'admin.totp.enable' : 'admin.totp.start') : mode === 'create' ? t('admin.tokens.create') : mode === 'disable' ? t('admin.totp.disable') : t('admin.save')}</Button><Button onClick={close}>{t('common.cancel')}</Button></div>
      </form>}
    </Dialog>
    <ConfirmDialog open={revoking !== null} onOpenChange={(value) => { if (!value) { if (revocation.pending) { setNotice(t('admin.action_unknown')); refresh(); } revocation.cancel(); setRevoking(null); } }} title={t('admin.revoke.title', { name: revoking?.name ?? '' })} description={t(revoking?.current ? 'admin.revoke.current' : 'admin.revoke.effect')} confirmLabel={t('admin.revoke')} cancelLabel={t('common.cancel')} destructive pending={revocation.pending} {...fieldError(revokeError)} onConfirm={() => {
      if (!revoking) return;
      const token = revoking;
      void revocation.run(async (signal) => { await api.DELETE('/api/v1/security/tokens/{id}', { signal, params: { path: { id: token.id } } }); }, () => {
        setRevoking(null);
        if (token.current) { clear(); setSignedOut(true); if (authentication) void authentication.refresh(); else void client.resetQueries({ queryKey: apiKey('me') }); }
        else { setNotice(t('admin.revoked.success')); refresh(); }
      });
    }} />
  </div>;
}
