import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { Navigate, useLocation, useNavigate } from '@tanstack/react-router';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { apiData, useApi, useApiError } from '../api/hooks';
import { ApiError } from '../api/errors';
import { apiKey } from '../api/keys';
import type { Schema } from '../api/types';
import { selectLocale, useTranslation } from '../i18n';
import { useTheme } from './theme';
import { AuthenticationContext } from './auth-state';
import { AppShell, PublicLayout } from './layout';
import { LoginForm, LoginPage } from '../features/onboarding';
import { Dialog } from '../components/ui/dialog';
import { Button } from '../components/ui/button';
import { InlineMessage } from '../components/ui/inline-message';
import { StatePanel } from '../components/ui/state-panel';
import { useLiveEvents } from './live-events';
import { useUnsavedAction } from './unsaved-changes';

function unauthenticated(error: unknown): boolean {
  return error instanceof ApiError && error.status === 401;
}

export function Gateway({ children }: { children: ReactNode }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const { t, setLocale } = useTranslation();
  const { setTheme } = useTheme();
  const describe = useApiError();
  const { pathname } = useLocation();
  const navigate = useNavigate();
  const confirmAction = useUnsavedAction();
  const [lost, setLost] = useState(false);
  const [reopenLogin, setReopenLogin] = useState(true);
  const [loggingOut, setLoggingOut] = useState(false);
  const [logoutError, setLogoutError] = useState<string>();
  const hadSession = useRef(false);
  const lastIdentity = useRef<Schema<'Me'> | undefined>(undefined);
  const setup = useQuery({ queryKey: apiKey('setup'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/setup', { signal })) });
  const me = useQuery({ queryKey: apiKey('me'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/me', { signal })), enabled: setup.data?.initialized === true });
  const identity = me.data ?? lastIdentity.current;
  const authenticated = identity !== undefined && !lost && !unauthenticated(me.error);
  useLiveEvents(authenticated);
  const preferences = useQuery({ queryKey: apiKey('settings'), queryFn: async ({ signal }) => apiData(await api.GET('/api/v1/settings', { signal })), enabled: authenticated });
  const publicEntry = pathname === '/setup' || pathname === '/login';

  useEffect(() => api.subscribeAuthenticationLost(() => {
    setLost(true);
    setReopenLogin(true);
    void queryClient.cancelQueries({ predicate: (query) => query.queryKey[0] === 'api' && query.queryKey[1] !== 'me' && query.queryKey[1] !== 'setup' });
  }), [api, queryClient]);

  useEffect(() => {
    if (authenticated) hadSession.current = true;
    if (me.data) lastIdentity.current = me.data;
  }, [authenticated, me.data]);

  const language = preferences.data?.values['general.language'] ?? me.data?.language;
  const appearance = preferences.data?.values['general.appearance'] ?? me.data?.appearance;
  useEffect(() => {
    if (language === 'system' || language === 'zh-CN' || language === 'en') void setLocale(selectLocale(language));
  }, [language, setLocale]);
  useEffect(() => {
    if (appearance === 'system' || appearance === 'light' || appearance === 'dark') setTheme(appearance);
  }, [appearance, setTheme]);

  const complete = useCallback((user: Schema<'Me'>) => {
    api.restoreAuthentication();
    lastIdentity.current = user;
    queryClient.setQueryData(apiKey('me'), user);
    queryClient.setQueryData<Schema<'SetupStatus'>>(apiKey('setup'), (previous) => previous ? { ...previous, initialized: true } : previous);
    setLost(false);
    setLogoutError(undefined);
    void queryClient.invalidateQueries({ queryKey: ['api'] });
    if (publicEntry) void navigate({ to: '/', replace: true });
  }, [api, navigate, publicEntry, queryClient]);

  const refresh = useCallback(() => {
    void queryClient.resetQueries({ queryKey: apiKey('me') });
  }, [queryClient]);

  async function logout() {
    setLoggingOut(true);
    setLogoutError(undefined);
    try {
      await api.POST('/api/v1/auth/logout');
      setLost(true);
      hadSession.current = false;
      lastIdentity.current = undefined;
      queryClient.clear();
      void navigate({ to: '/login', replace: true, ignoreBlocker: true });
    } catch (error) {
      setLogoutError(describe(error));
    } finally { setLoggingOut(false); }
  }

  const state = { authenticated, user: identity, complete, refresh };
  const warning = document.documentElement.dataset.kakattaPublicHttp === 'true'
    ? <InlineMessage tone="danger" title={t('shell.public_http_title')} className="mb-lg">{t('shell.public_http_description')}</InlineMessage>
    : null;
  const publicPage = (content: ReactNode) => <PublicLayout>{warning}{content}</PublicLayout>;
  if (setup.isPending || setup.data?.initialized && me.isPending && !identity) return publicPage(<StatePanel kind="pending" label={t('common.loading')} />);
  if (setup.isError && !setup.data) return publicPage(<StatePanel kind="error" title={t('web.error.title')} description={describe(setup.error)} retry={{ label: t('common.retry'), run: () => { void setup.refetch(); } }} />);
  if (!setup.data?.initialized && pathname !== '/setup') return <Navigate to="/setup" replace />;
  if (setup.data?.initialized && pathname === '/setup') return <Navigate to={authenticated ? '/' : '/login'} replace />;
  if (me.isError && !unauthenticated(me.error) && !lost && !identity) return publicPage(<StatePanel kind="error" title={t('web.error.title')} description={describe(me.error)} retry={{ label: t('common.retry'), run: refresh }} />);
  if (pathname === '/login' && authenticated) return <Navigate to="/" replace />;
  if (publicEntry) return <AuthenticationContext.Provider value={state}>{publicPage(children)}</AuthenticationContext.Provider>;
  if (!authenticated && !hadSession.current) return <AuthenticationContext.Provider value={state}>{publicPage(<LoginPage onAuthenticated={complete} />)}</AuthenticationContext.Provider>;
  if (!identity) return publicPage(<StatePanel kind="pending" label={t('common.loading')} />);

  const showLogin = !authenticated && reopenLogin;
  return (
    <AuthenticationContext.Provider value={state}>
      <AppShell user={identity} onLogout={() => confirmAction(() => { void logout(); })} loggingOut={loggingOut} logoutError={logoutError}>
        {!showLogin && warning}
        {me.isError && !unauthenticated(me.error) && !lost && <StatePanel kind="error" title={t('web.error.title')} description={describe(me.error)} retry={{ label: t('common.retry'), run: refresh }} />}
        <div hidden={!authenticated}>{children}</div>
        {!authenticated && <StatePanel kind="empty" title={t('shell.session_expired_title')} description={t('shell.session_expired_description')} action={<Button variant="primary" onClick={() => setReopenLogin(true)}>{t('shell.sign_in_again')}</Button>} />}
      </AppShell>
      <Dialog open={showLogin} onOpenChange={setReopenLogin} title={t('shell.session_expired_title')} description={t('shell.session_expired_description')} closeLabel={t('common.close')}>
        {showLogin && warning}
        <LoginForm sessionExpired onAuthenticated={complete} onCancel={() => setReopenLogin(false)} />
      </Dialog>
    </AuthenticationContext.Provider>
  );
}
