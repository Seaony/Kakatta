import { expect, test } from 'bun:test';
import type { ReactNode } from 'react';
import { QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createApiClient } from '../../api/client';
import type { ApiFetch } from '../../api/client';
import { ApiProvider } from '../../api/hooks';
import { apiKey } from '../../api/keys';
import type { Schema } from '../../api/types';
import { createAppQueryClient } from '../../app/query-client';
import { createI18n, I18nProvider } from '../../i18n';
import { LoginForm } from './login';
import { SetupPage } from './setup';

const identity: Schema<'Me'> = {
  username: 'local-admin', auth_kind: 'web_session', token_id: 7,
  language: 'zh-CN', appearance: 'system', totp_enabled: false,
};

function failure(status: number, code: string, args = {}, fields: Schema<'FieldIssue'>[] = []) {
  return Response.json({ error: { code, args }, fields, request_id: 'request-local-27' }, { status });
}

async function mount(node: ReactNode, transport: ApiFetch) {
  const queryClient = createAppQueryClient();
  const route = createRootRoute({ component: () => node });
  const router = createRouter({ routeTree: route, history: createMemoryHistory({ initialEntries: ['/'] }) });
  await router.load();
  const view = render(<I18nProvider instance={createI18n('zh-CN')}><QueryClientProvider client={queryClient}>
    <ApiProvider api={createApiClient({ fetch: transport })}><RouterProvider router={router} /></ApiProvider>
  </QueryClientProvider></I18nProvider>);
  return { queryClient, view, close() { view.unmount(); queryClient.clear(); } };
}

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

test('初始化严格提交三字段，真实响应后直接登录且清除秘密', async () => {
  const bodies: unknown[] = [];
  const users: Schema<'Me'>[] = [];
  const page = await mount(<SetupPage onAuthenticated={(user) => users.push(user)} />, async (request) => {
    if (request.method === 'GET') return Response.json({ initialized: false, token_expires_at: Date.now() + 1_800_000 });
    bodies.push(await request.json());
    return Response.json(identity);
  });
  try {
    await userEvent.type(await screen.findByLabelText('一次性初始化令牌'), 'token-local-52');
    await userEvent.type(screen.getByLabelText('用户名'), 'local-admin');
    await userEvent.type(screen.getByLabelText('登录密码'), '  unchanged password  ');
    await userEvent.click(screen.getByRole('button', { name: '建立管理员并继续' }));
    await waitFor(() => expect(users).toEqual([identity]));
    expect(bodies).toEqual([{ token: 'token-local-52', username: 'local-admin', password: '  unchanged password  ' }]);
    expect(screen.queryByLabelText('一次性初始化令牌')).toBeNull();
    expect(screen.queryByLabelText('登录密码')).toBeNull();
    expect(page.queryClient.getQueryData<Schema<'SetupStatus'>>(apiKey('setup'))).toEqual({ initialized: true, token_expires_at: null });
    expect(JSON.stringify(page.queryClient.getQueryCache().getAll().map((query) => query.state.data))).not.toContain('token-local-52');
  } finally { page.close(); }
});

test('初始化状态读取失败不是未初始化，显式重查后才显示表单', async () => {
  let calls = 0;
  const page = await mount(<SetupPage />, async () => {
    calls++;
    return calls === 1 ? failure(503, 'private.unknown') : Response.json({ initialized: false, token_expires_at: null });
  });
  try {
    await screen.findByText('请求未完成，请重试。');
    expect(screen.queryByLabelText('一次性初始化令牌')).toBeNull();
    expect(calls).toBe(1);
    await userEvent.click(screen.getByRole('button', { name: '重试' }));
    await screen.findByLabelText('一次性初始化令牌');
    expect(calls).toBe(2);
    expect(screen.getByText('暂时无法取得令牌到期时间，提交时会由服务端检查。')).toBeTruthy();
  } finally { page.close(); }
});

test('并发初始化冲突可重新检查并转普通登录，不覆盖已有管理员', async () => {
  let initialized = false;
  let writes = 0;
  const page = await mount(<SetupPage />, async (request) => {
    const path = new URL(request.url).pathname;
    if (path === '/api/v1/me') return failure(401, 'auth.unauthenticated');
    if (request.method === 'GET') return Response.json({ initialized, token_expires_at: initialized ? null : Date.now() + 1_800_000 });
    initialized = true; writes++;
    return failure(409, 'auth.already_initialized');
  });
  try {
    await userEvent.type(await screen.findByLabelText('一次性初始化令牌'), 'token-local-52');
    await userEvent.type(screen.getByLabelText('用户名'), 'local-admin');
    await userEvent.type(screen.getByLabelText('登录密码'), 'password-local-86');
    await userEvent.click(screen.getByRole('button', { name: '建立管理员并继续' }));
    await screen.findByText('已经完成初始化');
    await userEvent.click(screen.getByRole('button', { name: '重新检查状态' }));
    await screen.findByRole('link', { name: '登录' });
    expect(screen.queryByLabelText('一次性初始化令牌')).toBeNull();
    expect(writes).toBe(1);
  } finally { page.close(); }
});

test('过期初始化令牌禁用提交，只提供重新检查而不自动获取新令牌', async () => {
  let requests = 0;
  const page = await mount(<SetupPage />, async () => {
    requests++;
    return Response.json({ initialized: false, token_expires_at: Date.now() - 50_000 });
  });
  try {
    await screen.findByText('初始化令牌已过期，请重启容器后重试');
    expect(screen.getByRole('button', { name: '建立管理员并继续' }).hasAttribute('disabled')).toBe(true);
    expect(screen.getByLabelText('一次性初始化令牌').hasAttribute('disabled')).toBe(true);
    expect(requests).toBe(1);
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
