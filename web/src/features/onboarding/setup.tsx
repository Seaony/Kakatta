import { useEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { apiData, useApi } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import type { Schema } from '../../api/types';
import { Button, InlineMessage, PageHeader, SecretField, Skeleton, StatePanel, TextField } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { AuthError, useAuthFieldError, useAuthFieldFocus } from './auth-error';
import { ApiError } from '../../api/errors';
import { useCredentialAction, useCurrentTime } from './auth-state';

export function SetupPage({ onAuthenticated }: { onAuthenticated?: (user: Schema<'Me'>) => void }) {
  const { t } = useTranslation();
  const api = useApi();
  const queryClient = useQueryClient();
  const setup = useQuery({ queryKey: apiKey('setup'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/setup', { signal })) });
  const action = useCredentialAction();
  const fieldError = useAuthFieldError(action.error);
  const [token, setToken] = useState('');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [user, setUser] = useState<Schema<'Me'> | null>(null);
  const composing = useRef(false);
  const formRef = useRef<HTMLFormElement>(null);
  useAuthFieldFocus(action.error, formRef);
  const expiresAt = setup.data?.token_expires_at;
  const now = useCurrentTime(typeof expiresAt === 'number' && !setup.data?.initialized);
  const expired = typeof expiresAt === 'number' && expiresAt <= now;
  const statusChanged = action.error instanceof ApiError && ['auth.already_initialized', 'auth.setup_expired'].includes(action.error.reason.code);

  useEffect(() => {
    if (setup.data?.initialized) { setToken(''); setPassword(''); }
  }, [setup.data?.initialized]);

  function authenticated(identity: Schema<'Me'>) {
    setToken('');
    setPassword('');
    setUser(identity);
    queryClient.setQueryData(apiKey('me'), identity);
    queryClient.setQueryData(apiKey('setup'), { initialized: true, token_expires_at: null } satisfies Schema<'SetupStatus'>);
    onAuthenticated?.(identity);
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (composing.current || expired) return;
    const identity = await action.run(async (signal) => apiData(await api.POST('/api/v1/setup', {
      signal, body: { token, username, password },
    })));
    if (identity) authenticated(identity);
  }

  async function checkOutcome() {
    const status = await setup.refetch();
    if (status.data?.initialized) {
      const identity = await action.run(async (signal) => apiData(await api.GET('/api/v1/me', { signal })));
      if (identity) authenticated(identity);
    }
  }

  return (
    <div className="mx-auto w-full min-w-0 max-w-xl space-y-xl">
      <PageHeader title={t('web.setup.title')} description={t('web.setup.token_help')} />
      {user ? <InlineMessage tone="positive" action={<Link to="/onboarding">{t('web.onboarding.continue')}</Link>}>{t('web.auth.signed_in')}</InlineMessage>
        : setup.isPending ? <StatePanel kind="pending" label={t('common.loading')}><Skeleton /><Skeleton /></StatePanel>
          : setup.data?.initialized ? <InlineMessage tone="info" action={<Link to="/login">{t('web.auth.sign_in')}</Link>}>{t('web.setup.already_done')}</InlineMessage>
            : setup.data ? (
              <form ref={formRef} className="space-y-lg" onSubmit={(event) => { void submit(event); }}
                onKeyDown={(event) => { if (event.key === 'Enter' && event.nativeEvent.isComposing) event.preventDefault(); }}
                onCompositionStart={() => { composing.current = true; }} onCompositionEnd={() => { composing.current = false; }}>
                {expired ? <InlineMessage tone="warning" action={<Button onClick={() => { void setup.refetch(); }} disabled={setup.isFetching}>{t('web.auth.check_status')}</Button>}>{t('auth.setup_expired')}</InlineMessage>
                  : <p className="text-body text-text-secondary">{typeof expiresAt === 'number' ? t('web.setup.token_remaining', { count: Math.max(0, Math.ceil((expiresAt - now) / 1000)) }) : t('web.setup.token_expiry_unknown')}</p>}
                <SecretField label={t('web.setup.token')} name="token" value={token} autoComplete="off" spellCheck={false}
                  showLabel={t('web.auth.show_secret')} hideLabel={t('web.auth.hide_secret')} required disabled={action.pending || expired}
                  onChange={(event) => setToken(event.target.value)} {...fieldError('token')} />
                <TextField label={t('web.auth.username')} name="username" value={username} autoComplete="username" required
                  disabled={action.pending || expired} onChange={(event) => setUsername(event.target.value)} {...fieldError('username')} />
                <SecretField label={t('web.auth.password')} name="password" value={password} autoComplete="new-password" required
                  showLabel={t('web.auth.show_password')} hideLabel={t('web.auth.hide_password')} disabled={action.pending || expired}
                  onChange={(event) => setPassword(event.target.value)} {...fieldError('password')} />
                <AuthError error={action.error} />
                {statusChanged && <Button onClick={() => { void checkOutcome(); }} disabled={action.pending || setup.isFetching}>{t('web.auth.check_status')}</Button>}
                {action.waitSeconds > 0 && <p role="status" className="text-body text-text-secondary">{t('auth.rate_limited', { seconds: action.waitSeconds })}</p>}
                {action.uncertain && <InlineMessage tone="warning" action={<Button onClick={() => { void checkOutcome(); }} disabled={action.pending || setup.isFetching}>{t('web.auth.check_status')}</Button>}>{t('web.request_uncertain')}</InlineMessage>}
                <div className="flex flex-wrap gap-sm">
                  <Button type="submit" variant="primary" busy={action.pending} busyLabel={t('web.setup.submitting')} disabled={expired || action.waitSeconds > 0}>{t('web.setup.submit')}</Button>
                  {action.pending && <Button onClick={action.cancel}>{t('common.cancel')}</Button>}
                </div>
              </form>
            ) : null}
      {setup.error && <div className="space-y-md"><AuthError error={setup.error} /><Button onClick={() => { void setup.refetch(); }}>{t('common.retry')}</Button></div>}
    </div>
  );
}
