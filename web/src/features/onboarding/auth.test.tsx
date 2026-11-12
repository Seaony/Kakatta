import { expect, spyOn, test } from 'bun:test';
import type { ReactNode } from 'react';
import { QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createApiClient } from '../../api/client';
import type { ApiFetch } from '../../api/client';
import { ApiProvider } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import type { Schema } from '../../api/types';
import { createAppQueryClient } from '../../app/query-client';
import { AuthenticationContext } from '../../app/auth-state';
import { createI18n, I18nProvider, type Locale } from '../../i18n';
import { LoginForm } from './login';
import { SetupPage } from './setup';

const identity: Schema<'Me'> = {
  username: 'local-admin', auth_kind: 'web_session', token_id: 7,
  language: 'zh-CN', appearance: 'system', totp_enabled: false,
};

function failure(status: number, code: string, args = {}, fields: Schema<'FieldIssue'>[] = []) {
  return Response.json({ error: { code, args }, fields, request_id: 'request-local-27' }, { status });
}

async function mount(node: ReactNode, transport: ApiFetch, locale: Locale = 'zh-CN') {
  const queryClient = createAppQueryClient();
  const route = createRootRoute({ component: () => node });
  const router = createRouter({ routeTree: route, history: createMemoryHistory({ initialEntries: ['/'] }) });
  await router.load();
  const view = render(<I18nProvider instance={createI18n(locale)}><QueryClientProvider client={queryClient}>
    <ApiProvider api={createApiClient({ fetch: transport })}><RouterProvider router={router} /></ApiProvider>
  </QueryClientProvider></I18nProvider>);
  return { queryClient, view, close() { view.unmount(); queryClient.clear(); } };
}

test.each(['zh-CN', 'en', 'ja'] as const)('%s 新实例只设置用户名和密码，随后可用同一账号正常登录', async (locale) => {
  const bodies: unknown[] = [], users: Schema<'Me'>[] = [];
  const i18n = createI18n(locale);
  const transport: ApiFetch = async (request) => {
    if (request.method === 'GET') return Response.json({ initialized: false });
    bodies.push(await request.json());
    return Response.json(new URL(request.url).pathname.endsWith('/login') ? { status: 'authenticated', user: identity } : identity);
  };
  const page = await mount(<SetupPage onAuthenticated={(user) => users.push(user)} />, transport, locale);
  try {
    const username = await screen.findByLabelText(i18n.t('web.auth.username'));
    const password = screen.getByLabelText(i18n.t('web.auth.password'));
    expect([...username.closest('form')!.querySelectorAll('input')].map(input => input.name)).toEqual(['username', 'password']);
    await userEvent.type(username, 'local-admin'); await userEvent.type(password, '  unchanged password  ');
    await userEvent.click(screen.getByRole('button', { name: i18n.t('web.setup.submit') }));
    await waitFor(() => expect(users).toEqual([identity]));
    expect(bodies).toEqual([{ username: 'local-admin', password: '  unchanged password  ' }]);
    expect(page.queryClient.getQueryData<Schema<'SetupStatus'>>(apiKey('setup'))).toEqual({ initialized: true });
    expect(page.queryClient.getMutationCache().getAll()).toHaveLength(0);
  } finally { page.close(); }
  const login = await mount(<LoginForm onAuthenticated={(user) => users.push(user)} />, transport, locale);
  try {
    await userEvent.type(screen.getByLabelText(i18n.t('web.auth.username')), 'local-admin');
    await userEvent.type(screen.getByLabelText(i18n.t('web.auth.password')), '  unchanged password  ');
    await userEvent.click(screen.getByRole('button', { name: i18n.t('web.auth.sign_in') }));
    await waitFor(() => expect(users).toEqual([identity, identity]));
    expect(bodies).toEqual(Array(2).fill({ username: 'local-admin', password: '  unchanged password  ' }));
  } finally { login.close(); }
});

function backupPreview(): Schema<'BackupRestorePreview'> {
  return {
    ticket: 'local-first-restore-ticket', expires_at: Date.now() + 60_000,
    manifest: {
      format: 'kakatta.backup', version: 1, app_version: '1.2.3', schema_version: 4, created_at: '2026-11-11T00:00:00Z',
      kdf: { name: 'argon2id', salt: 'bG9jYWxmaXh0dXJlMTIzNA==', time: 3, memory_kib: 65536, threads: 1 },
    },
  };
}

test.each(['zh-CN', 'en', 'ja'] as const)('%s 新实例可匿名验证备份，保留覆盖确认后转普通登录', async (locale) => {
  const i18n = createI18n(locale), calls: Request[] = [];
  const navigate = spyOn(window.location, 'replace').mockImplementation(() => {});
  const page = await mount(<AuthenticationContext.Provider value={{ authenticated: false, user: undefined, complete() {}, refresh() {} }}>
    <SetupPage />
  </AuthenticationContext.Provider>, async (request) => {
    calls.push(request.clone());
    const path = new URL(request.url).pathname;
    if (path === '/api/v1/setup') return Response.json({ initialized: false });
    if (path === '/api/v1/backup/restore') return Response.json(backupPreview());
    if (path === '/api/v1/backup/confirm') return Response.json({ restored: true });
    throw new Error('未声明的本机假请求：' + path);
  }, locale);
  try {
    await userEvent.type(await screen.findByLabelText(i18n.t('web.auth.password')), 'discarded-new-password');
    await userEvent.click(screen.getByRole('button', { name: i18n.t('web.setup.restore_backup') }));
    expect(Boolean(screen.queryByRole('heading', { name: i18n.t('admin.backup.export') }))).toBe(false);
    await userEvent.click(screen.getByRole('button', { name: i18n.t('web.setup.back_to_account') }));
    expect(screen.getByLabelText<HTMLInputElement>(i18n.t('web.auth.password')).value).toBe('');
    await userEvent.click(screen.getByRole('button', { name: i18n.t('web.setup.restore_backup') }));
    page.queryClient.setQueryData(['api', 'findings'], { old: '旧数据' });
    const file = screen.getByLabelText<HTMLInputElement>(i18n.t('admin.backup.file'));
    const password = screen.getByLabelText<HTMLInputElement>(i18n.t('admin.backup.restore_password'));
    await userEvent.upload(file, new File(['local-first-restore-fixture'], 'first-backup.zip', { type: 'application/zip' }));
    await userEvent.type(password, 'original-backup-password');
    await userEvent.click(screen.getByRole('button', { name: i18n.t('admin.backup.validate') }));
    const dialog = await screen.findByRole('dialog', { name: i18n.t('admin.backup.preview_title') });
    expect(within(dialog).getByText(i18n.t('admin.backup.overwrite'))).toBeTruthy();
    expect(within(dialog).getByText(i18n.t('admin.backup.confirm_usage'))).toBeTruthy();
    expect(within(dialog).getByText(i18n.t('admin.backup.step_sign_in'))).toBeTruthy();
    expect(within(dialog).getAllByText(/\(UTC\)/)).toHaveLength(2);
    expect(file.files?.length).toBe(0);
    expect(password.value).toBe('');
    const upload = calls.find(request => new URL(request.url).pathname === '/api/v1/backup/restore');
    const body = await upload!.formData();
    expect([...body.keys()].sort()).toEqual(['file', 'password']);
    expect(body.get('password')).toBe('original-backup-password');
    const uploaded = body.get('file') as File;
    expect(uploaded.name).toBe('first-backup.zip');
    expect(await uploaded.text()).toBe('local-first-restore-fixture');
    expect(calls.filter(request => new URL(request.url).pathname === '/api/v1/backup/confirm')).toHaveLength(0);
    expect(JSON.stringify(page.queryClient.getQueryCache().getAll().map(query => query.state.data))).not.toContain('local-first-restore-ticket');
    await userEvent.click(within(dialog).getByRole('button', { name: i18n.t('admin.backup.confirm_action') }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith('/login'));
    const confirms = calls.filter(request => new URL(request.url).pathname === '/api/v1/backup/confirm');
    expect(confirms).toHaveLength(1);
    expect(await confirms[0]!.json()).toEqual({ ticket: 'local-first-restore-ticket' });
    expect(page.queryClient.getQueryData(['api', 'findings'])).toBeUndefined();
    expect(calls.every(request => ['/api/v1/setup', '/api/v1/backup/restore', '/api/v1/backup/confirm'].includes(new URL(request.url).pathname))).toBe(true);
  } finally { page.close(); navigate.mockRestore(); }
});

test('匿名恢复时管理员已在别处建立，会重查初始化状态并转登录', async () => {
  let initialized = false;
  const page = await mount(<SetupPage />, async (request) => {
    if (new URL(request.url).pathname === '/api/v1/setup') return Response.json({ initialized });
    initialized = true;
    return failure(401, 'auth.unauthenticated');
  });
  try {
    await userEvent.click(await screen.findByRole('button', { name: '从备份恢复' }));
    await userEvent.upload(screen.getByLabelText('加密备份 ZIP 文件'), new File(['local'], 'first-backup.zip', { type: 'application/zip' }));
    await userEvent.type(screen.getByLabelText('此备份的解密密码'), 'original-backup-password');
    await userEvent.click(screen.getByRole('button', { name: '验证备份' }));
    await screen.findByRole('link', { name: '登录' });
    expect(Boolean(screen.queryByLabelText('此备份的解密密码'))).toBe(false);
    expect(Boolean(screen.queryByLabelText('用户名'))).toBe(false);
  } finally { page.close(); }
});

test('密码失败保留输入并定位字段，未知原因安全回退，不自动重复登录', async () => {
  let requests = 0;
  const bodies: unknown[] = [];
  const page = await mount(<LoginForm onAuthenticated={() => { throw new Error('不应登录成功'); }} />, async (request) => {
    requests++;
    bodies.push(await request.json());
    return failure(400, 'private.unknown', {}, [{ key: 'username', reason: { code: 'auth.invalid_input', args: {} } }]);
  });
  try {
    await userEvent.type(screen.getByLabelText('用户名'), 'local-admin');
    await userEvent.type(screen.getByLabelText('登录密码'), '  keep spaces  ');
    await userEvent.click(screen.getByRole('button', { name: '登录' }));
    await waitFor(() => expect(screen.getByText('请求未完成，请重试。')).toBeTruthy());
    expect(requests).toBe(1);
    expect(bodies).toEqual([{ username: 'local-admin', password: '  keep spaces  ' }]);
    expect(screen.getByLabelText('登录密码').getAttribute('value')).toBe('  keep spaces  ');
    expect(document.activeElement).toBe(screen.getByLabelText('用户名'));
    expect(screen.getByText('请求编号：request-local-27')).toBeTruthy();
    expect(document.body.textContent).not.toContain('private.unknown');
    expect(page.queryClient.getMutationCache().getAll()).toHaveLength(0);
  } finally { page.close(); }
});

test('两步登录保留验证码开头零，挑战和密码不入Query缓存，仅验证成功才回调', async () => {
  const requests: { path: string; body: unknown }[] = [];
  const authenticated: Schema<'Me'>[] = [];
  const page = await mount(<LoginForm onAuthenticated={(user) => authenticated.push(user)} />, async (request) => {
    const path = new URL(request.url).pathname;
    const body: unknown = await request.json();
    requests.push({ path, body });
    return path.endsWith('/login')
      ? Response.json({ status: 'totp_required', challenge: 'challenge-local-91', expires_at: Date.now() + 300_000 })
      : Response.json({ ...identity, totp_enabled: true });
  });
  try {
    await userEvent.type(screen.getByLabelText('用户名'), 'local-admin');
    await userEvent.type(screen.getByLabelText('登录密码'), 'password-local-86');
    await userEvent.click(screen.getByRole('button', { name: '登录' }));
    const code = await screen.findByLabelText('六位验证码');
    expect(authenticated).toHaveLength(0);
    expect(screen.queryByLabelText('登录密码')).toBeNull();
    expect(code.getAttribute('type')).not.toBe('number');
    expect(document.activeElement).toBe(code);
    const cached = JSON.stringify(page.queryClient.getQueryCache().getAll().map((query) => query.state.data));
    expect(cached).not.toContain('challenge-local-91');
    expect(cached).not.toContain('password-local-86');
    await userEvent.type(code, '001237');
    expect(requests).toHaveLength(1);
    await userEvent.click(screen.getByRole('button', { name: '验证并登录' }));
    await waitFor(() => expect(authenticated).toHaveLength(1));
    expect(requests[1]).toEqual({ path: '/api/v1/auth/totp', body: { challenge: 'challenge-local-91', code: '001237' } });
    expect(page.queryClient.getQueryData<Schema<'Me'>>(apiKey('me'))).toEqual({ ...identity, totp_enabled: true });
    expect(page.queryClient.getMutationCache().getAll()).toHaveLength(0);
  } finally { page.close(); }
});

test('挑战失效后重新输入密码，不能继续复用旧挑战', async () => {
  let calls = 0;
  const page = await mount(<LoginForm onAuthenticated={() => { throw new Error('不应登录成功'); }} />, async () => {
    calls++;
    return calls === 1 ? Response.json({ status: 'totp_required', challenge: 'old-local-challenge', expires_at: Date.now() + 300_000 }) : failure(401, 'auth.challenge_expired');
  });
  try {
    await userEvent.type(screen.getByLabelText('用户名'), 'local-admin');
    await userEvent.type(screen.getByLabelText('登录密码'), 'password-local-86');
    await userEvent.click(screen.getByRole('button', { name: '登录' }));
    await userEvent.type(await screen.findByLabelText('六位验证码'), '987321');
    await userEvent.click(screen.getByRole('button', { name: '验证并登录' }));
    await waitFor(() => expect(screen.queryByLabelText('六位验证码')).toBeNull());
    expect(screen.getByLabelText('用户名').getAttribute('value')).toBe('local-admin');
    expect(screen.getByLabelText('登录密码').getAttribute('value')).toBe('');
    expect(calls).toBe(2);
  } finally { page.close(); }
});

test('登录保护等待结束只解禁按钮，不自动重新提交密码', async () => {
  let calls = 0;
  const page = await mount(<LoginForm onAuthenticated={() => { throw new Error('不应登录成功'); }} />, async () => {
    calls++;
    return failure(429, 'auth.rate_limited', { seconds: 1 });
  });
  try {
    await userEvent.type(screen.getByLabelText('用户名'), 'local-admin');
    await userEvent.type(screen.getByLabelText('登录密码'), 'password-local-86');
    await userEvent.click(screen.getByRole('button', { name: '登录' }));
    await waitFor(() => expect(screen.getByRole('button', { name: '登录' }).hasAttribute('disabled')).toBe(true));
    await waitFor(() => expect(screen.getByRole('button', { name: '登录' }).hasAttribute('disabled')).toBe(false), { timeout: 2500 });
    expect(calls).toBe(1);
    expect(screen.getByLabelText('登录密码').getAttribute('value')).toBe('password-local-86');
  } finally { page.close(); }
});

test('取消不宣称回滚，只读核实会话后可继续，不重发凭据', async () => {
  let loginCalls = 0;
  let meCalls = 0;
  let signal: AbortSignal | undefined;
  const users: Schema<'Me'>[] = [];
  const page = await mount(<LoginForm onAuthenticated={(user) => users.push(user)} />, async (request) => {
    if (new URL(request.url).pathname.endsWith('/me')) { meCalls++; return Response.json(identity); }
    loginCalls++;
    signal = request.signal;
    return new Promise<Response>((_resolve, reject) => request.signal.addEventListener('abort', () => reject(new DOMException('cancelled', 'AbortError')), { once: true }));
  });
  try {
    await userEvent.type(screen.getByLabelText('用户名'), 'local-admin');
    await userEvent.type(screen.getByLabelText('登录密码'), 'password-local-86');
    await userEvent.click(screen.getByRole('button', { name: '登录' }));
    const form = screen.getByLabelText('用户名').closest('form');
    expect(form).toBeTruthy();
    if (form) fireEvent.submit(form);
    expect(loginCalls).toBe(1);
    await userEvent.click(screen.getByRole('button', { name: '取消' }));
    expect(signal?.aborted).toBe(true);
    expect(screen.getByText('尚未确认操作结果，请先检查已保存的内容。')).toBeTruthy();
    expect(users).toHaveLength(0);
    await userEvent.click(screen.getByRole('button', { name: '重新检查状态' }));
    await waitFor(() => expect(users).toHaveLength(1));
    expect(loginCalls).toBe(1);
    expect(meCalls).toBe(1);
  } finally { page.close(); }
});

test('初始化状态读取失败不是未初始化，显式重查后才显示表单', async () => {
  let calls = 0;
  const page = await mount(<SetupPage />, async () => {
    calls++;
    return calls === 1 ? failure(503, 'private.unknown') : Response.json({ initialized: false });
  });
  try {
    await screen.findByText('请求未完成，请重试。');
    expect(screen.queryByLabelText('用户名')).toBeNull();
    expect(calls).toBe(1);
    await userEvent.click(screen.getByRole('button', { name: '重试' }));
    await screen.findByLabelText('用户名');
    expect(calls).toBe(2);
  } finally { page.close(); }
});

test('并发初始化冲突可重新检查并转普通登录，不覆盖已有管理员', async () => {
  let initialized = false;
  let writes = 0;
  const page = await mount(<SetupPage />, async (request) => {
    const path = new URL(request.url).pathname;
    if (path === '/api/v1/me') return failure(401, 'auth.unauthenticated');
    if (request.method === 'GET') return Response.json({ initialized });
    initialized = true; writes++;
    return failure(409, 'auth.already_initialized');
  });
  try {
    await userEvent.type(await screen.findByLabelText('用户名'), 'local-admin');
    await userEvent.type(screen.getByLabelText('登录密码'), 'password-local-86');
    await userEvent.click(screen.getByRole('button', { name: '建立管理员并继续' }));
    await screen.findByText('已经完成初始化');
    await userEvent.click(screen.getByRole('button', { name: '重新检查状态' }));
    await screen.findByRole('link', { name: '登录' });
    expect(screen.queryByLabelText('用户名')).toBeNull();
    expect(writes).toBe(1);
  } finally { page.close(); }
});

test('输入法确认期间不提交凭据，结束后仍需明确提交', async () => {
  let requests = 0;
  const page = await mount(<LoginForm onAuthenticated={() => {}} />, async () => {
    requests++;
    return failure(401, 'auth.invalid_credentials');
  });
  try {
    const username = screen.getByLabelText('用户名');
    await userEvent.type(username, 'local-admin');
    await userEvent.type(screen.getByLabelText('登录密码'), 'password-local-86');
    const form = username.closest('form');
    if (!form) throw new Error('缺少登录表单');
    fireEvent.compositionStart(username);
    fireEvent.submit(form);
    expect(requests).toBe(0);
    fireEvent.compositionEnd(username);
    expect(requests).toBe(0);
    await userEvent.click(screen.getByRole('button', { name: '登录' }));
    await screen.findByText('凭据不正确');
    expect(requests).toBe(1);
  } finally { page.close(); }
});
