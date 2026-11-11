import { expect, test } from 'bun:test';
import { StrictMode } from 'react';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createMemoryHistory } from '@tanstack/react-router';
import { createApiClient } from '../api/client';
import { createAppRouter } from '../app/router';
import { createAppQueryClient } from '../app/query-client';
import { AppProviders } from '../app/providers';


test.each([false, true])('正式完整路由首次初始化 / StrictMode=%s', async (strict) => {
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: undefined });
  let initialized = false;
  const requests: string[] = [];
  const identity = { username: 'review-owner', auth_kind: 'web_session', token_id: 1,
    language: 'zh-CN', appearance: 'system', totp_enabled: false };
  const api = createApiClient({ fetch: async (request) => {
    const path = new URL(request.url).pathname;
    requests.push(`${request.method} ${path}`);
    if (path === '/api/v1/setup') {
      if (request.method === 'POST') { initialized = true; return Response.json(identity); }
      return Response.json({ initialized, token_expires_at: initialized ? null : Date.now() + 1_800_000 });
    }
    if (path === '/api/v1/me') return Response.json(identity);
    if (path === '/api/v1/settings') return Response.json({ values: { 'general.language': 'zh-CN',
      'general.appearance': 'system', 'general.onboarding_done': false, 'general.timezone': 'Asia/Shanghai' }, sources: {}, warnings: [] });
    if (['/api/v1/tasks', '/api/v1/accounts', '/api/v1/proxies'].includes(path)) return Response.json({ items: [], next_cursor: null, total: 0 });
    throw new Error('未声明的本机假请求：' + request.method + ' ' + path);
  } });
  const queryClient = createAppQueryClient();
  const history = createMemoryHistory({ initialEntries: ['/'] });
  const router = createAppRouter({ queryClient, api }, history);
  let events = 0;
  const off = router.subscribe('onBeforeNavigate', () => {
    events++;
    if (events > 20) { console.error('初始化导航没有收敛', { strict, events, path: router.state.location.pathname }); process.exit(21); }
  });
  await router.load();
  const app = <AppProviders router={router} queryClient={queryClient} initialLocale="zh-CN" initialTheme="system" />;
  const view = render(strict ? <StrictMode>{app}</StrictMode> : app);
  try {
    const user = userEvent.setup();
    await screen.findByRole('button', { name: '建立管理员并继续' });
    await user.type(screen.getByLabelText('一次性初始化令牌', { selector: 'input' }), 'temporary-local-token');
    await user.type(screen.getByRole('textbox', { name: '用户名' }), identity.username);
    await user.type(screen.getByLabelText('登录密码', { selector: 'input' }), 'LocalReviewPasswordOnly123');
    await user.click(screen.getByRole('button', { name: '建立管理员并继续' }));
    await waitFor(() => expect(router.state.location.pathname).toBe('/onboarding'), { timeout: 3000 });
    await waitFor(() => expect(document.querySelector('input[name="name"]') !== null).toBe(true), { timeout: 3000 });
    expect(requests).toContain('POST /api/v1/setup');
    expect(requests).toContain('GET /api/v1/accounts');
    expect(requests).toContain('GET /api/v1/tasks');
    expect(events).toBeLessThanOrEqual(20);
    console.log('FRESH_SETUP_RESULT ' + JSON.stringify({ strict, ready: true, events }));
  } finally {
    off(); view.unmount(); await queryClient.cancelQueries(); queryClient.clear();
  }
}, 8000);
