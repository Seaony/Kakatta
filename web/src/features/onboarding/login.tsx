import { useEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { ApiError } from '../../api/errors';
import { apiData, useApi } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import type { Schema } from '../../api/types';
import { Button, InlineMessage, PageHeader, SecretField, TextField } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { AuthError, useAuthFieldError, useAuthFieldFocus } from './auth-error';
import { useCredentialAction, useCurrentTime } from './auth-state';
import { RecoveryHelp } from './recovery-help';

export type LoginFormProps = {
  onAuthenticated(user: Schema<'Me'>): void;
  onCancel?: () => void;
  sessionExpired?: boolean;
};

export function LoginForm({ onAuthenticated, onCancel, sessionExpired = false }: LoginFormProps) {
  const { t } = useTranslation();
  const api = useApi();
  const queryClient = useQueryClient();
  const action = useCredentialAction();
  const fieldError = useAuthFieldError(action.error);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [challenge, setChallenge] = useState<Schema<'TOTPRequiredLogin'> | null>(null);
  const [code, setCode] = useState('');
  const [expired, setExpired] = useState(false);
  const codeRef = useRef<HTMLInputElement>(null);
  const usernameRef = useRef<HTMLInputElement>(null);
  const formRef = useRef<HTMLFormElement>(null);
  useAuthFieldFocus(action.error, formRef);
  const composing = useRef(false);
  const now = useCurrentTime(challenge !== null);
  const challengeExpired = challenge !== null && now >= challenge.expires_at;

  function returnToPassword() {
    setChallenge(null);
    setCode('');
    setPassword('');
    setExpired(false);
    action.clearError();
    usernameRef.current?.focus();
  }

  function authenticated(user: Schema<'Me'>) {
    setPassword('');
    setCode('');
    setChallenge(null);
    queryClient.setQueryData(apiKey('me'), user);
    onAuthenticated(user);
  }

  useEffect(() => {
    if (challenge) codeRef.current?.focus();
    else usernameRef.current?.focus();
  }, [challenge]);

  useEffect(() => {
    if (!challengeExpired || action.pending) return;
    setChallenge(null);
    setCode('');
    setPassword('');
    setExpired(true);
  }, [challengeExpired, action.pending]);

  useEffect(() => {
    if (action.error instanceof ApiError && action.error.reason.code === 'auth.challenge_expired') {
      setChallenge(null);
      setCode('');
      setPassword('');
      setExpired(true);
    }
  }, [action.error]);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (composing.current || challengeExpired) return;
    setExpired(false);
    if (challenge) {
      if (!/^[0-9]{6}$/.test(code)) return;
      const user = await action.run(async (signal) => apiData(await api.POST('/api/v1/auth/totp', {
        signal, body: { challenge: challenge.challenge, code },
      })));
      if (user) authenticated(user);
      return;
    }
    const result = await action.run(async (signal) => apiData(await api.POST('/api/v1/auth/login', {
      signal, body: { username, password },
    })));
    if (!result) return;
    setPassword('');
    if (result.status === 'authenticated') authenticated(result.user);
    else setChallenge(result);
  }

  async function checkOutcome() {
    const user = await action.run(async (signal) => {
      try { return apiData(await api.GET('/api/v1/me', { signal })); }
      catch (failure) {
        if (failure instanceof ApiError && failure.status === 401) {
          setChallenge(null);
          setCode('');
          setPassword('');
        }
        throw failure;
      }
    });
    if (user) authenticated(user);
  }

  return (
    <form ref={formRef} className="space-y-lg" onSubmit={(event) => { void submit(event); }}
      onKeyDown={(event) => { if (event.key === 'Enter' && event.nativeEvent.isComposing) event.preventDefault(); }}
      onCompositionStart={() => { composing.current = true; }} onCompositionEnd={() => { composing.current = false; }}>
      {sessionExpired && <InlineMessage tone="warning"><p>{t('web.auth.session_ended')}</p><p>{t('web.auth.draft_kept')}</p></InlineMessage>}
      {expired && <InlineMessage tone="warning">{t('auth.challenge_expired')}</InlineMessage>}
      {challenge ? (
        <>
          <p className="text-body text-text-secondary">{t('web.auth.totp_help')}</p>
          <TextField ref={codeRef} label={t('web.auth.code')} name="code" value={code} autoComplete="one-time-code"
            inputMode="numeric" pattern="[0-9]{6}" maxLength={6} required disabled={action.pending || challengeExpired}
            onChange={(event) => setCode(event.target.value)} {...fieldError('code')} />
          <p className="text-body tabular-nums text-text-secondary">
            {challengeExpired ? t('auth.challenge_expired') : t('web.auth.challenge_time', {
              duration: t('web.auth.seconds', { count: Math.max(0, Math.ceil((challenge.expires_at - now) / 1000)) }),
            })}
          </p>
        </>
      ) : (
        <>
          <TextField ref={usernameRef} label={t('web.auth.username')} name="username" value={username} autoComplete="username"
            required disabled={action.pending} onChange={(event) => setUsername(event.target.value)} {...fieldError('username')} />
          <SecretField label={t('web.auth.password')} name="password" value={password} autoComplete="current-password"
            showLabel={t('web.auth.show_password')} hideLabel={t('web.auth.hide_password')} required disabled={action.pending}
            onChange={(event) => setPassword(event.target.value)} {...fieldError('password')} />
        </>
      )}
      <AuthError error={action.error} />
      {action.waitSeconds > 0 && <p role="status" className="text-body text-text-secondary">{t('auth.rate_limited', { seconds: action.waitSeconds })}</p>}
      {action.uncertain && <InlineMessage tone="warning" action={<Button onClick={() => { void checkOutcome(); }} disabled={action.pending}>{t('web.auth.check_status')}</Button>}>{t('web.request_uncertain')}</InlineMessage>}
      <div className="flex flex-wrap items-center gap-sm">
        <Button type="submit" variant="primary" busy={action.pending} busyLabel={t('web.auth.signing_in')}
          disabled={action.waitSeconds > 0 || challengeExpired || (challenge !== null && !/^[0-9]{6}$/.test(code))}>
          {t(challenge ? 'web.auth.verify' : 'web.auth.sign_in')}
        </Button>
        {action.pending && <Button onClick={action.cancel}>{t('common.cancel')}</Button>}
        {challenge && <Button onClick={returnToPassword} disabled={action.pending}>{t('web.auth.back_to_password')}</Button>}
        {onCancel && <Button onClick={onCancel} disabled={action.pending}>{t('common.close')}</Button>}
      </div>
      <RecoveryHelp kind={challenge ? 'totp' : 'password'} />
    </form>
  );
}

export function LoginPage({ onAuthenticated }: { onAuthenticated?: (user: Schema<'Me'>) => void }) {
  const { t } = useTranslation();
  const [user, setUser] = useState<Schema<'Me'> | null>(null);
  return (
    <div className="mx-auto w-full min-w-0 max-w-xl space-y-xl">
      <PageHeader title={t('web.auth.sign_in')} description={t('web.auth.login_description')} />
      {user ? (
        <InlineMessage tone="positive" action={<Link to="/onboarding">{t('web.onboarding.continue')}</Link>}>{t('web.auth.signed_in')}</InlineMessage>
      ) : <LoginForm onAuthenticated={(identity) => { setUser(identity); onAuthenticated?.(identity); }} />}
    </div>
  );
}
