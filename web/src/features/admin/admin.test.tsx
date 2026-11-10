import { afterEach, expect, spyOn, test } from 'bun:test';
import { useState, type ComponentType } from 'react';
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createApiClient } from '../../api/client';
import { ApiProvider } from '../../api/hooks';
import { AuthenticationContext } from '../../app/auth-state';
import type { components } from '../../api/schema.gen';
import { createI18n, I18nProvider } from '../../i18n';
import { AboutPage, documentOperations } from './about';
import { BackupPage } from './backup';
import { SecurityPage } from './security';
import { SystemPage } from './system';

const at = 1_795_000_000_000;
const me: components['schemas']['Me'] = { username: '本地用户', auth_kind: 'web_session', token_id: 1, language: 'zh-CN', appearance: 'system', totp_enabled: false };
const device: components['schemas']['TokenMetadata'] = { id: 1, kind: 'web_session', name: '本地浏览器', created_at: at, last_used_at: at, expires_at: Date.now() + 86_400_000, revoked_at: null, current: true };
const settings: components['schemas']['SettingsSnapshot'] = { values: { 'general.timezone': 'Asia/Shanghai', 'security.trusted_proxies': [], 'general.update_check': true }, sources: { 'security.trusted_proxies': 'default' }, warnings: [] };
type Call = { method: string; path: string; query: URLSearchParams; body: string; signal: AbortSignal; request: Request };
const clients: QueryClient[] = [];
afterEach(() => { clients.splice(0).forEach((client) => client.clear()); });

function failure(status: number, code = 'http.unavailable', args = {}) {
  return Response.json({ error: { code, args }, fields: [], request_id: 'admin-local-test' }, { status });
}

async function page(Component: ComponentType, respond: (call: Call) => Response | undefined | Promise<Response | undefined> = () => undefined, locale: 'zh-CN' | 'en' = 'zh-CN') {
  const calls: Call[] = [];
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false }, mutations: { retry: false } } });
  clients.push(client);
  const api = createApiClient({ fetch: async (request) => {
    const url = new URL(request.url);
    const multipart = request.headers.get('Content-Type')?.startsWith('multipart/form-data');
    const body = request.body && !multipart ? await request.clone().text() : '';
    const call: Call = { method: request.method, path: url.pathname, query: url.searchParams, body, signal: request.signal, request };
    calls.push(call);
    const response = await respond(call);
    if (response) return response;
    if (call.path === '/api/v1/settings') return Response.json(settings);
    if (call.path === '/api/v1/settings/definitions') return Response.json([]);
    if (call.path === '/api/v1/me') return Response.json(me);
    if (call.path === '/api/v1/security/tokens') return Response.json({ tokens: [device] });
    return failure(404, 'http.not_found');
  } });
  const root = createRootRoute({ component: () => <Component /> });
  const router = createRouter({ routeTree: root, history: createMemoryHistory({ initialEntries: ['/'] }) });
  await router.load();
  const view = render(<I18nProvider instance={createI18n(locale)}><QueryClientProvider client={client}><ApiProvider api={api}><RouterProvider router={router} /></ApiProvider></QueryClientProvider></I18nProvider>);
  return { ...view, api, calls, client, user: userEvent.setup() };
}

test('修改密码保留凭据错误输入，成功后只发送准确字段且不缓存秘密', async () => {
  let rejected = true;
  const fixture = await page(SecurityPage, (call) => call.path === '/api/v1/security/password' ? rejected ? failure(401, 'auth.invalid_credentials') : new Response(null, { status: 204 }) : undefined);
  await fixture.user.click(await screen.findByRole('button', { name: '修改登录密码' }));
  const dialog = screen.getByRole('dialog', { name: '修改登录密码' });
  await fixture.user.type(within(dialog).getByLabelText('当前密码'), '当前私密密码');
  await fixture.user.type(within(dialog).getByLabelText('新密码'), '更新私密密码');
  await fixture.user.type(within(dialog).getByLabelText('再输一次新密码'), '不一致');
  await fixture.user.click(within(dialog).getByRole('button', { name: '保存' }));
  expect(screen.getByText('两次输入的密码不一致。')).toBeTruthy();
  expect(fixture.calls.filter((call) => call.method === 'POST')).toHaveLength(0);
  await fixture.user.clear(within(dialog).getByLabelText('再输一次新密码'));
  await fixture.user.type(within(dialog).getByLabelText('再输一次新密码'), '更新私密密码');
  await fixture.user.click(within(dialog).getByRole('button', { name: '保存' }));
  await screen.findByText('凭据不正确');
  expect(within(dialog).getByLabelText<HTMLInputElement>('当前密码').value).toBe('当前私密密码');
  expect(within(dialog).getByLabelText<HTMLInputElement>('新密码').value).toBe('更新私密密码');
  expect(screen.getByText('请求编号：admin-local-test')).toBeTruthy();
  rejected = false;
  await fixture.user.click(within(dialog).getByRole('button', { name: '保存' }));
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  expect(JSON.parse(fixture.calls.find((call) => call.path === '/api/v1/security/password')?.body ?? '{}')).toEqual({ old_password: '当前私密密码', new_password: '更新私密密码' });
  expect(fixture.client.getMutationCache().getAll()).toHaveLength(0);
  expect(JSON.stringify(fixture.client.getQueryCache().getAll().map((query) => query.state.data))).not.toContain('私密密码');
});

test('令牌明文只留在一次弹层，关闭后不可从列表和查询缓存找回', async () => {
  const fixture = await page(SecurityPage, (call) => call.path === '/api/v1/security/tokens' && call.method === 'POST' ? Response.json({ id: 2, token: 'local-only-issued-secret', created_at: at }, { status: 201 }) : undefined);
  await fixture.user.click(await screen.findByRole('button', { name: '创建令牌' }));
  const dialog = screen.getByRole('dialog', { name: '创建令牌' });
  await fixture.user.type(within(dialog).getByLabelText('令牌名称'), '家庭脚本');
  await fixture.user.click(within(dialog).getByRole('button', { name: '创建令牌' }));
  await waitFor(() => expect(within(dialog).getByLabelText<HTMLTextAreaElement>('接口令牌').value).toBe('local-only-issued-secret'));
  expect(fixture.calls.filter((call) => call.method === 'POST')).toHaveLength(1);
  expect(JSON.parse(fixture.calls.find((call) => call.method === 'POST')?.body ?? '{}')).toEqual({ name: '家庭脚本' });
  expect(JSON.stringify(fixture.client.getQueryCache().getAll().map((query) => query.state.data))).not.toContain('local-only-issued-secret');
  await fixture.user.click(within(dialog).getAllByRole('button', { name: '关闭' })[0]!);
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  expect(document.body.textContent).not.toContain('local-only-issued-secret');
});

test('两步验证使用本地二维码，启用保留前导零并发准确的绑定令牌', async () => {
  let enabled = false;
  const fixture = await page(SecurityPage, (call) => {
    if (call.path === '/api/v1/me') return Response.json({ ...me, totp_enabled: enabled });
    if (call.path === '/api/v1/security/totp/setup') return Response.json({ setup_token: 'one-binding', secret: 'JBSWY3DPEHPK3PXP', uri: 'otpauth://totp/Kakatta:local?secret=JBSWY3DPEHPK3PXP&issuer=Kakatta', expires_at: Date.now() + 60_000 });
    if (call.path === '/api/v1/security/totp/enable') { enabled = true; return new Response(null, { status: 204 }); }
    return undefined;
  });
  await fixture.user.click(await screen.findByRole('button', { name: '绑定验证器' }));
  const dialog = screen.getByRole('dialog', { name: '绑定验证器' });
  await fixture.user.type(within(dialog).getByLabelText('当前密码'), 'local-password');
  await fixture.user.click(within(dialog).getByRole('button', { name: '绑定验证器' }));
  const input = await within(dialog).findByLabelText('六位验证码');
  const qr = await within(dialog).findByRole('img', { name: '用验证器扫描二维码' });
  expect(qr.getAttribute('src')).toStartWith('data:image/svg+xml;');
  await fixture.user.type(input, '012345');
  await fixture.user.click(within(dialog).getByRole('button', { name: '验证并开启' }));
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  expect(JSON.parse(fixture.calls.find((call) => call.path.endsWith('/totp/enable'))?.body ?? '{}')).toEqual({ setup_token: 'one-binding', code: '012345' });
  expect(JSON.stringify(fixture.client.getQueryCache().getAll().map((query) => query.state.data))).not.toContain('JBSWY3DPEHPK3PXP');
  expect(await screen.findByRole('button', { name: '关闭两步验证' })).toBeTruthy();
});

test('取消等待阻止迟到的秘密进入新弹层，也不自动重发创建动作', async () => {
  let finish: (response: Response) => void = () => {};
  const fixture = await page(SecurityPage, (call) => call.method === 'POST' && call.path.endsWith('/tokens') ? new Promise<Response>((resolve) => { finish = resolve; }) : undefined);
  await fixture.user.click(await screen.findByRole('button', { name: '创建令牌' }));
  const dialog = screen.getByRole('dialog');
  await fixture.user.type(within(dialog).getByLabelText('令牌名称'), '待取消');
  await fixture.user.click(within(dialog).getByRole('button', { name: '创建令牌' }));
  await waitFor(() => expect(fixture.calls.filter((call) => call.method === 'POST')).toHaveLength(1));
  expect(within(dialog).getByLabelText<HTMLInputElement>('令牌名称').disabled).toBe(true);
  await fixture.user.click(within(dialog).getByRole('button', { name: '取消' }));
  finish(Response.json({ id: 2, token: 'late-secret-must-be-discarded', created_at: at }, { status: 201 }));
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  expect(document.body.textContent).not.toContain('late-secret-must-be-discarded');
  expect(fixture.calls.find((call) => call.method === 'POST')?.signal.aborted).toBe(true);
  expect(fixture.calls.filter((call) => call.method === 'POST')).toHaveLength(1);
});

test('安全限流只允许等待后手动再试，不清输入或自动重交', async () => {
  let attempts = 0;
  const fixture = await page(SecurityPage, (call) => {
    if (call.path !== '/api/v1/security/password') return undefined;
    attempts++;
    return attempts === 1 ? failure(429, 'auth.rate_limited', { seconds: 1 }) : new Response(null, { status: 204 });
  });
  await fixture.user.click(await screen.findByRole('button', { name: '修改登录密码' }));
  const dialog = screen.getByRole('dialog');
  await fixture.user.type(within(dialog).getByLabelText('当前密码'), 'local-old');
  await fixture.user.type(within(dialog).getByLabelText('新密码'), 'local-new');
  await fixture.user.type(within(dialog).getByLabelText('再输一次新密码'), 'local-new');
  const save = within(dialog).getByRole<HTMLButtonElement>('button', { name: '保存' });
  await fixture.user.click(save);
  await waitFor(() => expect(save.disabled).toBe(true));
  expect(within(dialog).getByLabelText<HTMLInputElement>('新密码').value).toBe('local-new');
  await waitFor(() => expect(save.disabled).toBe(false), { timeout: 2500 });
  expect(attempts).toBe(1);
  await fixture.user.click(save);
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  expect(attempts).toBe(2);
});

test('绑定到期清除密钥和验证码，不自动重新申请绑定', async () => {
  const fixture = await page(SecurityPage, (call) => call.path.endsWith('/totp/setup') ? Response.json({ setup_token: 'expiring-binding', secret: 'JBSWY3DPEHPK3PXP', uri: 'otpauth://totp/local?secret=JBSWY3DPEHPK3PXP', expires_at: Date.now() + 200 }) : undefined);
  await fixture.user.click(await screen.findByRole('button', { name: '绑定验证器' }));
  const dialog = screen.getByRole('dialog');
  await fixture.user.type(within(dialog).getByLabelText('当前密码'), 'local-password');
  await fixture.user.click(within(dialog).getByRole('button', { name: '绑定验证器' }));
  await within(dialog).findByLabelText('手动绑定密钥');
  await within(dialog).findByText('绑定已过期，请重新开始。');
  expect(within(dialog).queryByLabelText('手动绑定密钥')).toBeNull();
  expect(within(dialog).queryByLabelText('六位验证码')).toBeNull();
  expect(fixture.calls.filter((call) => call.path.endsWith('/totp/setup'))).toHaveLength(1);
});

test('关闭两步验证发送当前密码和六位码，身份失效会清理待绑定秘密', async () => {
  const fixture = await page(SecurityPage, (call) => {
    if (call.path === '/api/v1/me') return Response.json({ ...me, totp_enabled: true });
    if (call.path.endsWith('/totp/disable')) return new Response(null, { status: 204 });
    return undefined;
  });
  await fixture.user.click(await screen.findByRole('button', { name: '关闭两步验证' }));
  const dialog = screen.getByRole('dialog');
  await fixture.user.type(within(dialog).getByLabelText('当前密码'), 'local-password');
  await fixture.user.type(within(dialog).getByLabelText('六位验证码'), '001234');
  await fixture.user.click(within(dialog).getByRole('button', { name: '关闭两步验证' }));
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  expect(JSON.parse(fixture.calls.find((call) => call.path.endsWith('/totp/disable'))?.body ?? '{}')).toEqual({ password: 'local-password', code: '001234' });
  fixture.unmount();
  function Protected() {
    const [authenticated, setAuthenticated] = useState(true);
    return <AuthenticationContext.Provider value={{ authenticated, user: authenticated ? me : undefined, complete() {}, refresh() {} }}><button onClick={() => setAuthenticated(false)}>模拟身份失效</button><SecurityPage /></AuthenticationContext.Provider>;
  }
  const next = await page(Protected, (call) => call.path.endsWith('/totp/setup') ? Response.json({ setup_token: 'discard-setup', secret: 'JBSWY3DPEHPK3PXP', uri: 'otpauth://totp/local?secret=JBSWY3DPEHPK3PXP', expires_at: Date.now() + 60_000 }) : undefined);
  await next.user.click(await screen.findByRole('button', { name: '绑定验证器' }));
  const bindingDialog = screen.getByRole('dialog');
  await next.user.type(within(bindingDialog).getByLabelText('当前密码'), 'local-password');
  await next.user.click(within(bindingDialog).getByRole('button', { name: '绑定验证器' }));
  await within(bindingDialog).findByLabelText('手动绑定密钥');
  fireEvent.click(screen.getByRole('button', { name: '模拟身份失效', hidden: true }));
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  expect(document.body.textContent).not.toContain('JBSWY3DPEHPK3PXP');
});

test('可信代理保存只提交该设置，字段错误保留逐行输入', async () => {
  let reject = true;
  const fixture = await page(SecurityPage, (call) => {
    if (call.path === '/api/v1/settings' && call.method === 'PATCH') return reject ? Response.json({ error: { code: 'settings.invalid', args: {} }, fields: [{ key: 'security.trusted_proxies', reason: { code: 'http.invalid_request', args: {} } }], request_id: 'proxy-test' }, { status: 422 }) : Response.json({ ...settings, values: { ...settings.values, 'security.trusted_proxies': ['10.0.0.0/8', '127.0.0.1'] } });
    return undefined;
  });
  const input = await screen.findByLabelText<HTMLTextAreaElement>('IP 或网段，每行一个');
  await fixture.user.type(input, '10.0.0.0/8\n127.0.0.1');
  await fixture.user.click(screen.getByRole('button', { name: '保存' }));
  await screen.findByText('请求内容无效。');
  expect(input.value).toBe('10.0.0.0/8\n127.0.0.1');
  reject = false;
  await fixture.user.click(screen.getByRole('button', { name: '保存' }));
  await screen.findByText('已保存');
  expect(JSON.parse(fixture.calls.find((call) => call.method === 'PATCH')?.body ?? '{}')).toEqual({ values: { 'security.trusted_proxies': ['10.0.0.0/8', '127.0.0.1'] } });
});

test('当前设备吊销成功后隐藏受保护动作，可信代理环境覆盖保持只读', async () => {
  const fixture = await page(SecurityPage, (call) => {
    if (call.path === '/api/v1/settings') return Response.json({ ...settings, values: { ...settings.values, 'security.trusted_proxies': ['10.0.0.0/8'] }, sources: { 'security.trusted_proxies': 'environment' } });
    if (call.method === 'DELETE') return new Response(null, { status: 204 });
    return undefined;
  });
  await waitFor(() => expect(screen.getByLabelText<HTMLTextAreaElement>('IP 或网段，每行一个').disabled).toBe(true));
  await fixture.user.click(await screen.findByRole('button', { name: '吊销「本地浏览器」' }));
  const dialog = screen.getByRole('alertdialog');
  expect(within(dialog).getByText('吊销当前设备后，你会退出登录。')).toBeTruthy();
  await fixture.user.click(within(dialog).getByRole('button', { name: '吊销' }));
  await screen.findByText('凭据已吊销。');
  expect(screen.queryByRole('button', { name: '修改登录密码' })).toBeNull();
  expect(fixture.calls.some((call) => call.method === 'DELETE' && call.path === '/api/v1/security/tokens/1' && call.body === '')).toBe(true);
});

test('系统页显示真实额度与未知资源，health 503与空闲浏览器各自明确', async () => {
  const data: components['schemas']['SystemStatus'] = { as_of: at, scheduler: { running: true, accepting: true, running_count: 0, queued_count: 2, next_wake_at: null }, browsers: { active: 0, limit: 1, sessions: [] }, configuration: [], resources: { disk_free_bytes: null, disk_total_bytes: null, memory_total_bytes: 1073741824, memory_used_bytes: 0 }, quotas: [{ account_id: 1, name: '本地测试账号', quota: { as_of: at, day_start_at: at - 1000, hourly_used: 35, hourly_limit: 30, daily_search_used: 0, daily_search_limit: 200, daily_detail_used: 2, daily_detail_limit: 100 } }] };
  const fixture = await page(SystemPage, (call) => {
    if (call.path === '/healthz') return Response.json({ status: 'unavailable' }, { status: 503 });
    if (call.path === '/api/v1/system/status') return Response.json(data);
    if (call.path === '/api/v1/system/events') return Response.json({ items: [], next_cursor: null, total: 0 });
    return undefined;
  });
  await screen.findByText('服务不可用');
  expect(await screen.findByText('35 / 30')).toBeTruthy();
  expect(screen.getByText('空闲时浏览器会关闭，当前没有活动会话。')).toBeTruthy();
  expect(screen.getAllByText('暂无记录').length).toBeGreaterThanOrEqual(2);
  expect(screen.queryByText('服务端返回了无法读取的响应。')).toBeNull();
  const events = fixture.calls.find((call) => call.path === '/api/v1/system/events');
  expect(events?.query.get('resolved')).toBe('false');
  expect(events?.query.get('limit')).toBe('50');
  expect(fixture.calls.every((call) => call.method === 'GET')).toBe(true);
});

test('导出只提交备份密码，收到有效 ZIP 后下载并清空密码', async () => {
  let downloaded: { filename: string; href: string } | undefined;
  const click = spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
    downloaded = { filename: this.download, href: this.href };
  });
  const fixture = await page(BackupPage, (call) => call.path === '/api/v1/backup/export'
    ? new Response(new Blob(['local-encrypted-fixture'], { type: 'application/zip' }), { headers: { 'Content-Type': 'application/zip', 'Content-Disposition': 'attachment; filename="local-backup.zip"' } })
    : undefined);
  try {
    const password = screen.getByLabelText<HTMLInputElement>('备份密码');
    const confirm = screen.getByLabelText<HTMLInputElement>('再输一次备份密码');
    const submit = screen.getByRole<HTMLButtonElement>('button', { name: '生成加密备份' });
    await fixture.user.type(password, 'local-backup-password');
    await fixture.user.type(confirm, 'different');
    expect(screen.getByText('两次输入的密码不一致。')).toBeTruthy();
    expect(submit.disabled).toBe(true);
    expect(fixture.calls.filter((call) => call.method === 'POST')).toHaveLength(0);
    await fixture.user.clear(confirm);
    await fixture.user.type(confirm, 'local-backup-password');
    await fixture.user.click(submit);
    await screen.findByText('已开始下载加密备份。');
    expect(JSON.parse(fixture.calls.find((call) => call.path === '/api/v1/backup/export')?.body ?? '{}')).toEqual({ password: 'local-backup-password' });
    expect(downloaded?.filename).toBe('local-backup.zip');
    expect(downloaded?.href).toStartWith('blob:');
    expect(password.value).toBe('');
    expect(confirm.value).toBe('');
    expect(fixture.client.getMutationCache().getAll()).toHaveLength(0);
  } finally { click.mockRestore(); }
});

function restorePreview(expiresAt = Date.now() + 60_000): components['schemas']['BackupRestorePreview'] {
  return {
    ticket: 'local-restore-ticket', expires_at: expiresAt,
    manifest: {
      format: 'kakatta.backup', version: 1, app_version: '1.2.3', schema_version: 4, created_at: '2026-11-11T00:00:00Z',
      kdf: { name: 'argon2id', salt: 'bG9jYWxmaXh0dXJlMTIzNA==', time: 3, memory_kib: 65536, threads: 1 },
    },
  };
}

async function selectRestore(user: ReturnType<typeof userEvent.setup>) {
  const file = screen.getByLabelText<HTMLInputElement>('加密备份 ZIP 文件');
  const password = screen.getByLabelText<HTMLInputElement>('此备份的解密密码');
  await user.upload(file, new File(['local-only-fixture'], 'local-backup.zip', { type: 'application/zip' }));
  await user.type(password, 'local-restore-password');
  await user.click(screen.getByRole('button', { name: '验证备份' }));
  return { file, password };
}

test('恢复提交原文件与解密密码，核对清单后单次确认并重新登录', async () => {
  let resolveConfirm: (response: Response) => void = () => {};
  const navigate = spyOn(window.location, 'replace').mockImplementation(() => {});
  function ProtectedBackup() {
    const [authenticated, setAuthenticated] = useState(true);
    return <AuthenticationContext.Provider value={{ authenticated, user: authenticated ? me : undefined, complete() {}, refresh() {} }}>
      <button onClick={() => setAuthenticated(false)}>模拟身份失效</button>
      <BackupPage />
    </AuthenticationContext.Provider>;
  }
  const fixture = await page(ProtectedBackup, (call) => {
    if (call.path === '/api/v1/backup/restore') return Response.json(restorePreview());
    if (call.path === '/api/v1/backup/confirm') return new Promise<Response>((resolve) => { resolveConfirm = resolve; });
    return undefined;
  });
  try {
    fixture.client.setQueryData(['api', 'findings'], { old: '旧数据' });
    const { file, password } = await selectRestore(fixture.user);
    const dialog = await screen.findByRole('dialog', { name: '备份已验证' });
    expect(within(dialog).getByText('1.2.3')).toBeTruthy();
    expect(within(dialog).getByText('4')).toBeTruthy();
    expect(within(dialog).getByText('访问额度和当天的 AI 用量会按备份里的记录重新计算。')).toBeTruthy();
    expect(file.files?.length).toBe(0);
    expect(password.value).toBe('');
    const upload = fixture.calls.find((call) => call.path === '/api/v1/backup/restore');
    if (!upload) throw new Error('缺少备份上传请求');
    expect(upload.request.headers.get('Content-Type')).toContain('boundary=');
    const body = await upload.request.clone().formData();
    expect([...body.keys()].sort()).toEqual(['file', 'password']);
    expect(body.get('password')).toBe('local-restore-password');
    const uploaded = body.get('file');
    if (!uploaded || typeof uploaded === 'string') throw new Error('文件未按原文件上传');
    expect(uploaded.name).toBe('local-backup.zip');
    expect(await uploaded.text()).toBe('local-only-fixture');
    expect(fixture.calls.some((call) => call.path === '/api/v1/backup/confirm')).toBe(false);
    expect(JSON.stringify(fixture.client.getQueryCache().getAll().map((query) => query.state.data))).not.toContain('local-restore-ticket');
    await fixture.user.dblClick(within(dialog).getByRole('button', { name: '确认覆盖并恢复' }));
    await screen.findByText('正在恢复备份，完成后需重新登录。');
    const confirms = fixture.calls.filter((call) => call.path === '/api/v1/backup/confirm');
    expect(confirms).toHaveLength(1);
    expect(JSON.parse(confirms[0]?.body ?? '{}')).toEqual({ ticket: 'local-restore-ticket' });
    await fixture.user.click(screen.getByRole('button', { name: '模拟身份失效' }));
    expect(confirms[0]?.signal.aborted).toBe(false);
    expect(navigate).not.toHaveBeenCalled();
    resolveConfirm(Response.json({ restored: true }));
    await screen.findByText('已恢复备份，请使用备份里的登录资料重新登录。');
    expect(navigate).toHaveBeenCalledWith('/login');
    expect(fixture.client.getQueryData(['api', 'findings'])).toBeUndefined();
  } finally { navigate.mockRestore(); }
});

test('取消验证会忽略迟到的候选，过期候选不能确认', async () => {
  let finish: (response: Response) => void = () => {};
  let requests = 0;
  const fixture = await page(BackupPage, (call) => {
    if (call.path !== '/api/v1/backup/restore') return undefined;
    requests++;
    return requests === 1 ? new Promise<Response>((resolve) => { finish = resolve; }) : Response.json(restorePreview(Date.now() - 1000));
  });
  const { file, password } = await selectRestore(fixture.user);
  await waitFor(() => expect(requests).toBe(1));
  await fixture.user.click(screen.getByRole('button', { name: '取消' }));
  finish(Response.json(restorePreview()));
  expect(fixture.calls.find((call) => call.path === '/api/v1/backup/restore')?.signal.aborted).toBe(true);
  expect(file.files?.length).toBe(1);
  expect(password.value).toBe('local-restore-password');
  expect(screen.queryByRole('dialog')).toBeNull();
  await fixture.user.click(screen.getByRole('button', { name: '验证备份' }));
  await screen.findByText('恢复确认已过期，请重新验证备份。');
  expect(screen.queryByRole('button', { name: '确认覆盖并恢复' })).toBeNull();
  expect(requests).toBe(2);
  expect(fixture.calls.some((call) => call.path === '/api/v1/backup/confirm')).toBe(false);
});

test('恢复确认返回 503 时保持结果未确认，禁止复用原票据', async () => {
  const fixture = await page(BackupPage, (call) => {
    if (call.path === '/api/v1/backup/restore') return Response.json(restorePreview());
    if (call.path === '/api/v1/backup/confirm') return failure(503, 'backup.io_failure');
    return undefined;
  });
  await selectRestore(fixture.user);
  await fixture.user.click(await screen.findByRole('button', { name: '确认覆盖并恢复' }));
  await screen.findByText('未能确认恢复结果。请先核对服务端状态，再决定是否重新恢复。');
  expect(screen.getByText('备份操作未能完成，请检查服务端状态。')).toBeTruthy();
  expect(screen.getByText('请求编号：admin-local-test')).toBeTruthy();
  expect(screen.queryByRole('button', { name: '确认覆盖并恢复' })).toBeNull();
  expect(screen.queryByText('已恢复备份，请使用备份里的登录资料重新登录。')).toBeNull();
  expect(fixture.calls.filter((call) => call.path === '/api/v1/backup/confirm')).toHaveLength(1);
});

test('恢复确认连接中断后展示未知结果，清空票据且不跳转登录', async () => {
  const navigate = spyOn(window.location, 'replace').mockImplementation(() => {});
  const fixture = await page(BackupPage, (call) => {
    if (call.path === '/api/v1/backup/restore') return Response.json(restorePreview());
    if (call.path === '/api/v1/backup/confirm') return Promise.reject(new TypeError('本地模拟断流'));
    return undefined;
  });
  try {
    await selectRestore(fixture.user);
    await fixture.user.click(await screen.findByRole('button', { name: '确认覆盖并恢复' }));
    await screen.findByText('未能确认恢复结果。请先核对服务端状态，再决定是否重新恢复。');
    expect(navigate).not.toHaveBeenCalled();
    expect(screen.queryByRole('button', { name: '确认覆盖并恢复' })).toBeNull();
    expect(fixture.calls.filter((call) => call.path === '/api/v1/backup/confirm')).toHaveLength(1);
  } finally { navigate.mockRestore(); }
});

test('服务端拒绝过期恢复票据，展示具体错误且不再次确认', async () => {
  const fixture = await page(BackupPage, (call) => {
    if (call.path === '/api/v1/backup/restore') return Response.json(restorePreview());
    if (call.path === '/api/v1/backup/confirm') return failure(409, 'backup.restore_expired');
    return undefined;
  });
  await selectRestore(fixture.user);
  await fixture.user.click(await screen.findByRole('button', { name: '确认覆盖并恢复' }));
  await screen.findByText('恢复确认已过期，请重新验证备份。');
  expect(screen.queryByText('未能确认恢复结果。请先核对服务端状态，再决定是否重新恢复。')).toBeNull();
  expect(screen.queryByRole('button', { name: '确认覆盖并恢复' })).toBeNull();
  expect(fixture.calls.filter((call) => call.path === '/api/v1/backup/confirm')).toHaveLength(1);
});

test('确认恢复时会话已失效，重新认证后必须重新验证文件', async () => {
  const fixture = await page(BackupPage, (call) => {
    if (call.path === '/api/v1/backup/restore') return Response.json(restorePreview());
    if (call.path === '/api/v1/backup/confirm') return failure(401, 'auth.unauthenticated');
    return undefined;
  });
  const { file, password } = await selectRestore(fixture.user);
  await fixture.user.click(await screen.findByRole('button', { name: '确认覆盖并恢复' }));
  await waitFor(() => expect(password.disabled).toBe(false));
  expect(file.files?.length).toBe(0);
  expect(password.value).toBe('');
  expect(screen.queryByRole('button', { name: '确认覆盖并恢复' })).toBeNull();
  fixture.api.restoreAuthentication();
  await selectRestore(fixture.user);
  await screen.findByRole('dialog', { name: '备份已验证' });
  expect(fixture.calls.filter((call) => call.path === '/api/v1/backup/restore')).toHaveLength(2);
  expect(fixture.calls.filter((call) => call.path === '/api/v1/backup/confirm')).toHaveLength(1);
});

test('备份上传验证错误保留待改输入，返回 401 后清除文件与密码', async () => {
  let invalid = true;
  const fixture = await page(BackupPage, (call) => {
    if (call.path !== '/api/v1/backup/restore') return undefined;
    if (!invalid) return failure(401, 'auth.unauthenticated');
    return Response.json({ error: { code: 'backup.authentication_failed', args: {} }, fields: [{ key: 'password', reason: { code: 'backup.authentication_failed', args: {} } }], request_id: 'local-upload' }, { status: 422 });
  });
  const { file, password } = await selectRestore(fixture.user);
  await screen.findByText('请求编号：local-upload');
  expect(screen.getAllByText('无法解密备份，请核对备份密码和文件。')).toHaveLength(2);
  expect(password.getAttribute('aria-invalid')).toBe('true');
  expect(password.value).toBe('local-restore-password');
  expect(file.files?.length).toBe(1);
  invalid = false;
  await fixture.user.click(screen.getByRole('button', { name: '验证备份' }));
  await waitFor(() => expect(password.value).toBe(''));
  expect(file.files?.length).toBe(0);
  expect(fixture.calls.filter((call) => call.path === '/api/v1/backup/restore')).toHaveLength(2);
  expect(fixture.calls.some((call) => call.path === '/api/v1/backup/confirm')).toBe(false);
});

test('备份导出收到错误的下载类型时不触发保存并保留输入', async () => {
  const click = spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
  const fixture = await page(BackupPage, (call) => call.path === '/api/v1/backup/export'
    ? Response.json({ unexpected: true }, { headers: { 'X-Request-Id': 'invalid-local-download' } })
    : undefined);
  try {
    await fixture.user.type(screen.getByLabelText('备份密码'), 'local-backup-password');
    await fixture.user.type(screen.getByLabelText('再输一次备份密码'), 'local-backup-password');
    await fixture.user.click(screen.getByRole('button', { name: '生成加密备份' }));
    await screen.findByText('服务端返回了无法读取的响应。');
    expect(screen.getByText('请求编号：invalid-local-download')).toBeTruthy();
    expect(click).not.toHaveBeenCalled();
    expect(screen.getByLabelText<HTMLInputElement>('备份密码').value).toBe('local-backup-password');
  } finally { click.mockRestore(); }
});

test('英文系统页使用单数名额，刷新失败保留旧读数并标明过时', async () => {
  let reads = 0;
  const data: components['schemas']['SystemStatus'] = { as_of: at, scheduler: { running: true, accepting: true, running_count: 0, queued_count: 2, next_wake_at: null }, browsers: { active: 0, limit: 1, sessions: [] }, configuration: [], quotas: [], resources: { disk_total_bytes: null, disk_free_bytes: null, memory_total_bytes: null, memory_used_bytes: null } };
  const fixture = await page(SystemPage, (call) => {
    if (call.path === '/healthz') return Response.json({ status: 'ok' });
    if (call.path === '/api/v1/system/events') return Response.json({ items: [], next_cursor: null, total: 0 });
    if (call.path === '/api/v1/system/status') { reads++; return reads === 1 ? Response.json(data) : failure(503); }
    return undefined;
  }, 'en');
  expect(await screen.findByText('0 of 1 page slot in use')).toBeTruthy();
  await fixture.user.click(screen.getByRole('button', { name: 'Refresh' }));
  await screen.findByText('Refresh failed. Showing the last data received.');
  expect(screen.getByText('0 of 1 page slot in use')).toBeTruthy();
  expect(reads).toBe(2);
});

test('备份页保持挂载时身份失效会清空三项密码和原生文件选择', async () => {
  function ProtectedBackup() {
    const [authenticated, setAuthenticated] = useState(true);
    return <AuthenticationContext.Provider value={{ authenticated, user: authenticated ? me : undefined, complete() {}, refresh() {} }}><button onClick={() => setAuthenticated(false)}>模拟身份失效</button><BackupPage /></AuthenticationContext.Provider>;
  }
  const fixture = await page(ProtectedBackup);
  const password = screen.getByLabelText<HTMLInputElement>('备份密码');
  const confirm = screen.getByLabelText<HTMLInputElement>('再输一次备份密码');
  const restorePassword = screen.getByLabelText<HTMLInputElement>('此备份的解密密码');
  const input = screen.getByLabelText<HTMLInputElement>('加密备份 ZIP 文件');
  await fixture.user.type(password, 'local-backup-secret');
  await fixture.user.type(confirm, 'local-backup-secret');
  await fixture.user.type(restorePassword, 'local-restore-secret');
  await fixture.user.upload(input, new File(['private-backup-fixture'], '需要清理的备份.zip', { type: 'application/zip' }));
  expect(input.files?.length).toBe(1);
  expect(screen.getByText('已选择：需要清理的备份.zip')).toBeTruthy();
  await fixture.user.click(screen.getByRole('button', { name: '模拟身份失效' }));
  await waitFor(() => expect(password.value).toBe(''));
  expect(confirm.value).toBe('');
  expect(restorePassword.value).toBe('');
  expect(input.value).toBe('');
  expect(input.files?.length).toBe(0);
  expect(screen.queryByText('已选择：需要清理的备份.zip')).toBeNull();
  expect(fixture.calls.some((call) => call.method === 'POST')).toBe(false);
});

const releaseURL = 'https://github.com/Seaony/Kakatta/releases/tag/v1.2.3';
const updateStatus: components['schemas']['UpdateStatus'] = {
  current_version: 'v1.2.2', enabled: true, error: null, known_release: { version: 'v1.2.3', url: releaseURL },
  last_attempt_at: at, last_success_at: at, next_check_at: at + 86_400_000, stale: false, update_available: true,
};

test('关于页读取真实版本与发布缓存，切换开关仅更新设置，接口文档可查且不请求 MCP', async () => {
  let updateEnabled = true;
  const document: components['schemas']['OpenAPIDocument'] = {
    openapi: '3.1.0', info: { version: 'interface-version' },
    paths: { '/api/v1/tasks': { get: { summary: '本地任务列表', security: [{ CookieSession: [] }], parameters: [{ name: 'limit', in: 'query' }], requestBody: { description: '本地请求字段' }, responses: { '200': { description: '本地响应' } } } } },
    components: { schemas: { Local: { type: 'object' } } },
  };
  const fixture = await page(AboutPage, (call) => {
    if (call.path === '/api/v1/openapi.json') return Response.json(document);
    if (call.path === '/api/v1/system/update') return Response.json(updateEnabled ? updateStatus : { ...updateStatus, enabled: false, next_check_at: null, update_available: null });
    if (call.path === '/api/v1/settings' && call.method === 'PATCH') {
      updateEnabled = false;
      return Response.json({ ...settings, values: { ...settings.values, 'general.update_check': false } });
    }
    return undefined;
  });
  expect(await screen.findByText('v1.2.2')).toBeTruthy();
  expect(await screen.findByText('有新版本 v1.2.3。')).toBeTruthy();
  expect(screen.getByRole('link', { name: '查看发布说明' }).getAttribute('href')).toBe(releaseURL);
  expect(screen.getByText('image: ghcr.io/seaony/kakatta:1.2.3')).toBeTruthy();
  expect(fixture.calls.some((call) => call.path === '/mcp')).toBe(false);
  expect(screen.queryByText('interface-version')).toBeNull();
  await fixture.user.click(await screen.findByRole('switch', { name: '自动检查新版本' }));
  await fixture.user.click(screen.getByRole('button', { name: '保存' }));
  await screen.findByText('已保存');
  expect(JSON.parse(fixture.calls.find((call) => call.method === 'PATCH')?.body ?? '{}')).toEqual({ values: { 'general.update_check': false } });
  await screen.findByText('已关闭自动检查。');
  await waitFor(() => expect(fixture.calls.filter((call) => call.path === '/api/v1/system/update').map((call) => call.method)).toEqual(['GET', 'GET']));
  expect(screen.queryByText('有新版本 v1.2.3。')).toBeNull();
  expect(screen.queryByText('image: ghcr.io/seaony/kakatta:1.2.3')).toBeNull();
  await fixture.user.click(screen.getByRole('button', { name: '查看接口文档' }));
  await screen.findByText('/api/v1/tasks');
  expect(screen.getByRole('link', { name: '下载原始 OpenAPI' }).getAttribute('href')).toBe('/api/v1/openapi.json');
  fireEvent.change(screen.getByLabelText('搜索方法、路径或说明'), { target: { value: 'missing-operation' } });
  expect(screen.getByText('没有匹配的接口。')).toBeTruthy();
  expect(fixture.calls.every((call) => call.path.startsWith('/api/v1/'))).toBe(true);
  expect(documentOperations(document)).toHaveLength(1);
  expect(JSON.stringify(documentOperations(document)[0]?.request)).toContain('parameters');
  expect(JSON.stringify(documentOperations(document)[0]?.request)).toContain('requestBody');
});

test('过期的发布缓存保留已知版本和发布说明，不提示已经发现新版', async () => {
  const fixture = await page(AboutPage, (call) => call.path === '/api/v1/system/update'
    ? Response.json({ ...updateStatus, stale: true, update_available: null, error: { code: 'update.check_failed', args: {} } })
    : undefined);
  expect(await screen.findByText('上次检查结果已过期。')).toBeTruthy();
  expect(screen.getByText('检查更新失败，稍后会自动重试。')).toBeTruthy();
  expect(screen.getByText('v1.2.3')).toBeTruthy();
  expect(screen.getByRole('link', { name: '查看发布说明' }).getAttribute('href')).toBe(releaseURL);
  expect(screen.queryByText('有新版本 v1.2.3。')).toBeNull();
  expect(screen.queryByText('image: ghcr.io/seaony/kakatta:1.2.3')).toBeNull();
  expect(fixture.calls.every((call) => call.method === 'GET')).toBe(true);
});
