import { afterEach, beforeEach, describe, expect, mock, spyOn, test } from 'bun:test';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClientProvider } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { Window as HappyDOMWindow } from 'happy-dom';
import { createApiClient } from '../../api/client';
import { ApiProvider } from '../../api/hooks';
import type { Schema } from '../../api/types';
import { createAppQueryClient } from '../../app/query-client';
import { AuthenticationContext } from '../../app/auth-state';
import { createI18n, I18nProvider } from '../../i18n';
import { AccountConnect, AccountsPage } from './index';
import { ManualSession, remotePath } from './manual-session';

let iframeLoadingDisabled = false;
let restoreConsole = () => {};
beforeEach(() => {
  if (window instanceof HappyDOMWindow) {
    iframeLoadingDisabled = window.happyDOM.settings.disableIframePageLoading;
    window.happyDOM.settings.disableIframePageLoading = true;
  }
  const originalError = console.error;
  const errors = spyOn(console, 'error').mockImplementation((...args: unknown[]) => {
    if (String(args[0]).includes('Iframe page loading is disabled.')) return;
    originalError(...args);
  });
  restoreConsole = () => errors.mockRestore();
});
afterEach(() => {
  if (window instanceof HappyDOMWindow) window.happyDOM.settings.disableIframePageLoading = iframeLoadingDisabled;
  restoreConsole();
});

const quota: Schema<'QuotaView'> = {
  hourly_used: 8, daily_search_used: 24, daily_detail_used: 12,
  hourly_limit: 30, daily_search_limit: 200, daily_detail_limit: 100,
  day_start_at: 1_790_000_000_000, as_of: 1_790_001_234_000,
};
function account(patch: Partial<Schema<'AccountView'>> = {}): Schema<'AccountView'> {
  return {
    id: 7, name: '家用网络', nickname: null, avatar_url: null, proxy_id: null, state: 'signed_out',
    state_reason: null, state_changed_at: null, cooldown_until: null, verify_level: 0,
    last_cooldown_end_at: null, cooldown_manual_used: false, relogin_after_pause: false, last_run_ended_at: null,
    next_gap_seconds: null, last_used_at: null, created_at: 1_790_000_000_000,
    quota, session: null, ...patch,
  };
}
function session(patch: Partial<Schema<'SessionView'>> = {}): Schema<'SessionView'> {
  return { id: 'session-opaque-7', account_id: 7, kind: 'login', phase: 'queued', run_id: null,
    task_id: null, queued_at: 1_790_001_200_000, started_at: null, expires_at: null, remote_path: null, viewport: null, ...patch };
}
const proxy: Schema<'ProxyView'> = { id: 11, name: '固定住宅网络', configured: true };
const json = (data: unknown, status = 200) => Response.json(data, { status });
const failure = (status: number, code: string, fields: Schema<'FieldIssue'>[] = []) => json({ error: { code, args: {} }, fields, request_id: 'local-request-7' }, status);

function mount(children: ReactNode, handle: (request: Request) => Promise<Response> | Response) {
  const queryClient = createAppQueryClient();
  const api = createApiClient({ fetch: async (request) => {
    const path = new URL(request.url).pathname;
    if (path === '/api/v1/settings') return json({ values: { 'general.timezone': 'Asia/Shanghai' }, sources: {}, warnings: [] });
    if (path === '/api/v1/proxies' && request.method === 'GET') return json({ items: [proxy], next_cursor: null, total: 1 });
    return handle(request);
  } });
  const view = render(<I18nProvider instance={createI18n()}><QueryClientProvider client={queryClient}><ApiProvider api={api}>{children}</ApiProvider></QueryClientProvider></I18nProvider>);
  return { ...view, queryClient };
}

describe('账号连接与人工会话', () => {
  test.each([
    ['退出后的拒绝暂停', 'denied', true, '暂停到 2026/11/11 10:30:00 (Asia/Shanghai)，之后需要重新扫码登录。', false],
    ['退出后的冷却', 'cooling', true, '暂停到 2026/11/11 10:30:00 (Asia/Shanghai)，之后需要重新扫码登录。', false],
    ['普通拒绝暂停', 'denied', false, '自动监控暂停到 2026/11/11 10:30:00 (Asia/Shanghai)。', false],
    ['普通冷却', 'cooling', false, '自动监控暂停到 2026/11/11 10:30:00 (Asia/Shanghai)。', true],
  ] as const)('%s显示对应的期限与手动运行提示', async (_label, state, reloginAfterPause, deadlineText, canRunManually) => {
    const current = account({ state, cooldown_until: Date.UTC(2026, 10, 11, 2, 30), relogin_after_pause: reloginAfterPause });
    const view = mount(<AccountsPage accountId={7} />, (request) => {
      const path = new URL(request.url).pathname;
      if (path === '/api/v1/accounts/7' && request.method === 'GET') return json(current);
      if (path === '/api/v1/accounts' && request.method === 'GET') return json({ items: [current], total: 1, next_cursor: null });
      return failure(404, 'http.not_found');
    });
    await screen.findByText(deadlineText);
    expect(screen.queryByText(/这次冷却期间可以手动运行一个任务一次/) !== null).toBe(canRunManually);
    expect(screen.queryByRole('button', { name: '重新扫码登录' })).toBeNull();
    view.unmount(); view.queryClient.clear();
  });

  test('已退出的冷却账号不能完成首次引导的连接步骤', async () => {
    let current = account({ state: 'cooling', cooldown_until: Date.UTC(2026, 10, 11, 2, 30), relogin_after_pause: true });
    const connected = mock((_account: Schema<'AccountView'>) => {});
    const view = mount(<AccountConnect initialAccountId={7} onConnected={connected} />, (request) => {
      const path = new URL(request.url).pathname;
      if (path === '/api/v1/accounts/7') return json(current);
      if (path === '/api/v1/accounts') return json({ items: [current], total: 1, next_cursor: null });
      return failure(404, 'http.not_found');
    });
    try {
      await screen.findByText(/之后需要重新扫码登录/);
      expect(connected).not.toHaveBeenCalled();
      current = { ...current, relogin_after_pause: false };
      await userEvent.click(screen.getByRole('button', { name: '刷新状态' }));
      await waitFor(() => expect(connected).toHaveBeenCalledTimes(1));
    } finally { view.unmount(); view.queryClient.clear(); }
  });

  test('六种账号状态均有文字，未知原因使用安全说明，创建失败保留输入并定位字段', async () => {
    const view = mount(<AccountConnect onConnected={() => {}} />, (request) => {
      if (request.method === 'GET') return json({ items: [], total: 0, next_cursor: null });
      return failure(422, 'future.account_error', [{ key: 'name', reason: { code: 'accounts.invalid', args: {} } }]);
    });
    const user = userEvent.setup();
    const name = await screen.findByLabelText('账号名称');
    await user.type(name, '保留这个名字');
    await user.click(screen.getByRole('button', { name: '保存账号' }));
    await waitFor(() => expect(name.getAttribute('aria-invalid')).toBe('true'));
    expect((name as HTMLInputElement).value).toBe('保留这个名字');
    expect(document.activeElement).toBe(name);
    expect(screen.getByText(/请求未完成，请重试/)).toBeTruthy();
    expect(screen.queryByText('future.account_error')).toBeNull();
    view.unmount(); view.queryClient.clear();

    const states: Schema<'AccountState'>[] = ['signed_out', 'active', 'cooling', 'needs_verification', 'denied', 'expired'];
    const list = mount(<AccountsPage />, (request) => request.method === 'GET'
      ? json({ items: states.map((state, index) => account({ id: index + 1, state })), total: states.length, next_cursor: null })
      : failure(404, 'http.not_found'));
    for (const label of ['未登录', '可用', '冷却中', '需要验证', '被拒绝访问', '已失效']) await screen.findByText(label);
    list.unmount(); list.queryClient.clear();
  });

  test('创建提交固定代理，202排队不等于连接成功，只有实际账号就绪才回调', async () => {
    let saved = false;
    let current = account();
    const writes: { method: string; path: string; body: unknown }[] = [];
    const connected = mock((_account: Schema<'AccountView'>) => {});
    const view = mount(<AccountConnect onConnected={connected} />, async (request) => {
      const path = new URL(request.url).pathname;
      if (request.method === 'GET' && path === '/api/v1/accounts') return json({ items: saved ? [current] : [], total: saved ? 1 : 0, next_cursor: null });
      if (request.method === 'GET' && path === '/api/v1/accounts/7') return json(current);
      if (request.method === 'GET' && path.endsWith('/sessions/session-opaque-7')) return current.session ? json(current.session) : failure(404, 'runtime.session_not_found');
      const body: unknown = request.body ? await request.json() : null;
      writes.push({ method: request.method, path, body });
      if (path === '/api/v1/accounts') { saved = true; current = account({ proxy_id: 11 }); return json(current, 201); }
      if (path.endsWith('/login')) { current = { ...current, session: session() }; return json(current.session, 202); }
      return failure(404, 'http.not_found');
    });
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText('账号名称'), '书房账号');
    await user.selectOptions(screen.getByLabelText('固定代理'), '11');
    await user.click(screen.getByRole('button', { name: '保存账号' }));
    expect(writes[0]).toEqual({ method: 'POST', path: '/api/v1/accounts', body: { name: '书房账号', proxy_id: 11 } });
    await user.click(await screen.findByRole('button', { name: '扫码登录' }));
    await screen.findByText('排队中');
    expect(connected).not.toHaveBeenCalled();
    expect(view.container.querySelector('iframe')).toBeNull();
    expect(writes.filter((item) => item.path.endsWith('/login'))).toHaveLength(1);
    current = { ...current, state: 'active', session: null };
    await user.click(screen.getByRole('button', { name: '刷新状态' }));
    await waitFor(() => expect(connected).toHaveBeenCalledTimes(1));
    expect(connected.mock.calls[0]?.[0]).toMatchObject({ state: 'active', session: null });
    view.unmount(); view.queryClient.clear();
  });

  test('读取验证保留不挂画面，显式接手后复用同会话，确认发送真实session_id', async () => {
    let currentSession = session({ kind: 'run', phase: 'verify_hold', remote_path: '/remote/sessions/session-opaque-7/', viewport: { x: 400, y: 200, width: 300, height: 200 } });
    let current = account({ state: 'needs_verification', session: currentSession });
    const confirmations: unknown[] = [];
    let accepted = 0;
    const view = mount(<AccountConnect initialAccountId={7} onConnected={() => { accepted += 1; }} />, async (request) => {
      const path = new URL(request.url).pathname;
      if (request.method === 'GET' && path === '/api/v1/accounts') return json({ items: [current], total: 1, next_cursor: null });
      if (request.method === 'GET' && path === '/api/v1/accounts/7') return json(current);
      if (request.method === 'GET' && path.includes('/sessions/')) return json(currentSession);
      if (path.endsWith('/verification/confirm')) {
        confirmations.push(await request.json());
        return json(current);
      }
      if (path.endsWith('/verification')) {
        currentSession = { ...currentSession, kind: 'verification', phase: 'verifying' };
        current = { ...current, session: currentSession };
        return json(currentSession, 202);
      }
      return failure(404, 'http.not_found');
    });
    const user = userEvent.setup();
    await screen.findByText('需要验证');
    expect(view.container.querySelector('iframe')).toBeNull();
    await user.click(screen.getByRole('button', { name: '去验证' }));
    await waitFor(() => expect(view.container.querySelector('iframe')?.getAttribute('src')).toBe('http://localhost/remote/sessions/session-opaque-7/'));
    await user.click(await screen.findByRole('button', { name: '我已完成验证' }));
    await waitFor(() => expect(confirmations).toEqual([{ session_id: 'session-opaque-7' }]));
    expect(accepted).toBe(0);
    expect(screen.getByText('需要验证')).toBeTruthy();
    view.unmount(); view.queryClient.clear();
  });

  test('重开无验证框和未恢复接口验证都提示在原画面手动刷新', async () => {
    const path = '/remote/sessions/session-opaque-7/';
    let current = session({ kind: 'verification', phase: 'verifying', remote_path: path, refresh_verification: true });
    const updated = mock(() => {});
    const requests: unknown[] = [];
    const view = mount(<ManualSession account={account({ state: 'needs_verification' })} session={current} timeZone="Asia/Shanghai"
      reveal onReveal={() => {}} onChanged={updated} onGone={() => {}} />, async (request) => {
      if (request.method === 'GET') return json(current);
      requests.push(await request.json());
      return failure(409, 'remote.verification_refresh_confirm');
    });
    const user = userEvent.setup();
    const firstHint = '当前画面没有验证框，请在画面里刷新一下页面。';
    await screen.findByText(firstHint);
    const frame = view.container.querySelector('iframe');
    expect(frame?.getAttribute('src')).toBe(`http://localhost${path}`);
    current = { ...current, refresh_verification: false };
    await user.click(screen.getByRole('button', { name: '重新读取会话' }));
    await waitFor(() => expect(screen.queryByText(firstHint)).toBeNull());
    await user.click(screen.getByRole('button', { name: '我已完成验证' }));
    await screen.findByText('页面可能还没刷新，请在画面里刷新一下页面，再点我已完成验证。');
    expect(requests).toEqual([{ session_id: 'session-opaque-7' }]);
    expect(view.container.querySelector('iframe')).toBe(frame);
    expect(updated).not.toHaveBeenCalled();
    view.unmount(); view.queryClient.clear();
  });

  test('结束会话使用匹配DELETE，退出闲鱼登录另经确认调用logout', async () => {
    let current = account({ state: 'active', session: session({ phase: 'waiting_login' }) });
    const writes: string[] = [];
    const view = mount(<AccountsPage accountId={7} />, (request) => {
      const path = new URL(request.url).pathname;
      if (request.method === 'GET' && path === '/api/v1/accounts') return json({ items: [current], total: 1, next_cursor: null });
      if (request.method === 'GET' && path === '/api/v1/accounts/7') return json(current);
      if (request.method === 'GET' && path.includes('/sessions/')) return current.session ? json(current.session) : failure(404, 'runtime.session_not_found');
      writes.push(`${request.method} ${path}`);
      if (request.method === 'DELETE') { current = { ...current, session: null }; return new Response(null, { status: 204 }); }
      if (path.endsWith('/logout')) { current = { ...current, state: 'signed_out' }; return json(current); }
      return failure(404, 'http.not_found');
    });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '结束会话' }));
    await waitFor(() => expect(writes).toEqual(['DELETE /api/v1/accounts/7/sessions/session-opaque-7']));
    await user.click(screen.getByRole('button', { name: '退出闲鱼登录' }));
    const dialog = await screen.findByRole('alertdialog');
    await user.click(within(dialog).getByRole('button', { name: '退出闲鱼登录' }));
    await waitFor(() => expect(writes).toContain('POST /api/v1/accounts/7/logout'));
    view.unmount(); view.queryClient.clear();
  });

  test('远程地址仅接受同源remote路径', () => {
    expect(remotePath('/remote/sessions/opaque/', 'https://kakatta.example')).toBe('https://kakatta.example/remote/sessions/opaque/');
    for (const value of ['https://other.example/remote/', '//other.example/remote/', '/api/v1/accounts', 'javascript:alert(1)', '/remote/../../login', null]) {
      expect(remotePath(value, 'https://kakatta.example')).toBeNull();
    }
  });

  test('间隔拒绝显示服务端预计时间，不创建排队会话', async () => {
    const current = account({ state: 'expired' });
    let loginCalls = 0;
    const view = mount(<AccountConnect initialAccountId={7} onConnected={() => {}} />, (request) => {
      const path = new URL(request.url).pathname;
      if (path === '/api/v1/accounts' && request.method === 'GET') return json({ items: [current], total: 1, next_cursor: null });
      if (path === '/api/v1/accounts/7' && request.method === 'GET') return json(current);
      loginCalls += 1;
      return json({ error: { code: 'run.account_gap', args: { next_allowed_at: 1_790_001_234_000 } }, fields: [], request_id: 'local-gap-7' }, 409);
    });
    await userEvent.setup().click(await screen.findByRole('button', { name: '重新扫码登录' }));
    await screen.findByText(/最早可在 .* 重试/);
    expect(loginCalls).toBe(1);
    expect(screen.queryByText('排队中')).toBeNull();
    expect(view.container.querySelector('iframe')).toBeNull();
    view.unmount(); view.queryClient.clear();
  });

  test('账号已删但资料清理失败时保留确认，可显式重试同一删除', async () => {
    let removed = false;
    let attempts = 0;
    const changed = mock((_id: number | undefined) => {});
    const view = mount(<AccountsPage accountId={7} onAccountChange={changed} />, (request) => {
      const path = new URL(request.url).pathname;
      if (request.method === 'GET' && path === '/api/v1/accounts') return json({ items: removed ? [] : [account()], total: removed ? 0 : 1, next_cursor: null });
      if (request.method === 'GET') return removed ? failure(404, 'accounts.not_found') : json(account());
      attempts += 1; removed = true;
      return attempts === 1 ? failure(503, 'accounts.cleanup_failed') : new Response(null, { status: 204 });
    });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '删除账号' }));
    const confirm = await screen.findByRole('alertdialog');
    await user.click(within(confirm).getByRole('button', { name: '删除账号' }));
    await waitFor(() => expect(within(confirm).getByText(/账号浏览器资料未能清理/)).toBeTruthy());
    await view.queryClient.invalidateQueries({ queryKey: ['api', 'accounts'] });
    expect(screen.getByRole('alertdialog')).toBeTruthy();
    await user.click(within(confirm).getByRole('button', { name: '删除账号' }));
    await waitFor(() => expect(changed).toHaveBeenCalledWith(undefined));
    expect(attempts).toBe(2);
    view.unmount(); view.queryClient.clear();
  });
});

describe('代理配置与失败恢复', () => {
  test('新代理字段失败保留本次输入，秘密不进入mutation缓存，明确重试才再次提交', async () => {
    const bodies: unknown[] = [];
    const view = mount(<AccountsPage />, async (request) => {
      if (request.method === 'GET') return json({ items: [], total: 0, next_cursor: null });
      bodies.push(await request.json());
      return bodies.length === 1 ? failure(422, 'proxies.invalid', [{ key: 'url', reason: { code: 'proxies.invalid_url', args: {} } }])
        : json({ id: 22, name: '备用网络', configured: true }, 201);
    });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '新增代理' }));
    const dialog = await screen.findByRole('dialog');
    await user.type(within(dialog).getByLabelText('代理名称'), '备用网络');
    const url = within(dialog).getByLabelText('代理地址');
    await user.type(url, 'http://fixture:password@proxy.example:8080');
    await user.click(within(dialog).getByRole('button', { name: '保存代理' }));
    await waitFor(() => expect(url.getAttribute('aria-invalid')).toBe('true'));
    expect((url as HTMLInputElement).value).toBe('http://fixture:password@proxy.example:8080');
    expect(bodies).toEqual([{ name: '备用网络', url: 'http://fixture:password@proxy.example:8080' }]);
    expect(view.queryClient.getMutationCache().getAll()).toHaveLength(0);
    await user.click(within(dialog).getByRole('button', { name: '保存代理' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(bodies).toHaveLength(2);
    view.unmount(); view.queryClient.clear();
  });

  test('编辑不发送空密码，连通测试false显示原因，409删除保持行和确认框', async () => {
    const writes: { method: string; path: string; body: unknown }[] = [];
    const view = mount(<AccountsPage />, async (request) => {
      const path = new URL(request.url).pathname;
      if (request.method === 'GET' && path === '/api/v1/accounts') return json({ items: [], total: 0, next_cursor: null });
      writes.push({ method: request.method, path, body: request.body ? await request.json() : null });
      if (request.method === 'PATCH') return json({ ...proxy, name: '固定住宅网络' });
      if (path.endsWith('/test')) return json({ ok: false, reason: { code: 'proxies.test_failed', args: {} } });
      if (request.method === 'DELETE') return failure(409, 'proxies.in_use');
      return failure(404, 'http.not_found');
    });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '编辑代理' }));
    const dialog = await screen.findByRole('dialog');
    expect((within(dialog).getByLabelText('代理地址') as HTMLInputElement).value).toBe('');
    await user.click(within(dialog).getByRole('button', { name: '保存代理' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(writes[0]).toEqual({ method: 'PATCH', path: '/api/v1/proxies/11', body: { name: '固定住宅网络' } });
    await user.click(screen.getByRole('button', { name: '测试连接' }));
    await screen.findByText('无法连接代理');
    expect(screen.queryByText('已连接到代理服务器，不代表能正常访问闲鱼。')).toBeNull();
    await user.click(screen.getByRole('button', { name: '删除代理' }));
    const confirm = await screen.findByRole('alertdialog');
    await user.click(within(confirm).getByRole('button', { name: '删除代理' }));
    await waitFor(() => expect(within(confirm).getByText(/代理仍在使用中，不能删除/)).toBeTruthy());
    expect(screen.getByRole('alertdialog')).toBeTruthy();
    expect(screen.getByText('固定住宅网络')).toBeTruthy();
    view.unmount(); view.queryClient.clear();
  });

  test('登录失效清代理秘密并卸载画面，不关闭后台会话也不再主动轮询', async () => {
    const current = account({ state: 'active', session: session({ phase: 'waiting_login', remote_path: '/remote/sessions/session-opaque-7/' }) });
    let reads = 0;
    const writes: string[] = [];
    function Example() {
      const [authenticated, setAuthenticated] = useState(true);
      return <AuthenticationContext.Provider value={{ authenticated, user: undefined, complete() {}, refresh() {} }}>
        <button type="button" onClick={() => setAuthenticated(false)}>模拟登录失效</button><AccountsPage accountId={7} />
      </AuthenticationContext.Provider>;
    }
    const view = mount(<Example />, (request) => {
      const path = new URL(request.url).pathname;
      if (request.method !== 'GET') { writes.push(`${request.method} ${path}`); return failure(401, 'auth.unauthenticated'); }
      reads += 1;
      if (path === '/api/v1/accounts') return json({ items: [current], total: 1, next_cursor: null });
      if (path === '/api/v1/accounts/7') return json(current);
      if (path.includes('/sessions/')) return json(current.session);
      return failure(404, 'http.not_found');
    });
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: '打开远程画面' }));
    await waitFor(() => expect(view.container.querySelector('iframe')).not.toBeNull());
    await user.click(screen.getByRole('button', { name: '编辑代理' }));
    const url = await screen.findByLabelText('代理地址');
    await user.type(url, 'http://private:secret@proxy.example:8080');
    fireEvent.click(screen.getByText('模拟登录失效'));
    await waitFor(() => expect((url as HTMLInputElement).value).toBe(''));
    expect(view.container.querySelector('iframe')).toBeNull();
    const count = reads;
    await new Promise((resolve) => setTimeout(resolve, 2_100));
    expect(reads).toBe(count);
    expect(writes).toEqual([]);
    view.unmount(); view.queryClient.clear();
  });
});
