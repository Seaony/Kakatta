import { afterEach, beforeEach, expect, test } from 'bun:test';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { DevelopmentConsole, type DevelopmentFetch, type DevelopmentStatus } from './console';

beforeEach(() => { document.documentElement.lang = 'zh-CN'; });
afterEach(() => { cleanup(); document.documentElement.removeAttribute('lang'); });

function status(values: Partial<DevelopmentStatus> = {}): DevelopmentStatus {
  return { mode: 'real', phase: 'running', generation: 3, scheduler_paused: true, last_error: '', logs: [], ...values };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

async function mount(fetcher: DevelopmentFetch, mode: 'fake' | 'real' = 'real', pollInterval = 100_000) {
  let view!: ReturnType<typeof render>;
  await act(async () => { view = render(<DevelopmentConsole mode={mode} fetcher={fetcher} pollInterval={pollInterval} />); });
  return view;
}

function expand() { fireEvent.click(screen.getByRole('button', { name: '展开开发控制台' })); }

test('状态返回前明确未知，不能开启调度；确认暂停后显示真实模式横幅', async () => {
  const reply = deferred<Response>();
  await mount(() => reply.promise);
  expect(screen.getByText('开发模式 · 调度状态未知')).toBeTruthy();
  expand();
  expect(screen.getByText('正在连接开发管理器')).toBeTruthy();
  expect(screen.getByRole('button', { name: '调度状态未知' }).hasAttribute('disabled')).toBe(true);
  await act(async () => { reply.resolve(Response.json(status())); });
  expect(await screen.findByText('开发模式 · 调度已暂停')).toBeTruthy();
  expect(screen.getByRole('button', { name: '开启调度' }).hasAttribute('disabled')).toBe(false);
  expect(screen.queryByRole('region', { name: '假数据故障' })).toBeNull();
});

test('首启后端未就绪时调度未知且禁用，管理器错误和日志仍可读', async () => {
  let writes = 0;
  await mount(async (_path, init) => {
    if (init?.method === 'POST') writes++;
    return Response.json({ ...status({ generation: 0, phase: 'error', scheduler_paused: false,
      last_error: '新后端启动时退出', logs: [{ seq: 1, at: 1794397920000, stream: 'stderr', text: '初始化令牌长度校验失败' }] }),
      backend_ready: false });
  });
  expand();
  expect(screen.getByText('开发模式 · 调度状态未知')).toBeTruthy();
  const button = screen.getByRole('button', { name: '调度状态未知' });
  expect(button.hasAttribute('disabled')).toBe(true);
  fireEvent.click(button);
  expect(writes).toBe(0);
  expect(screen.getByText('新后端启动时退出')).toBeTruthy();
  expect(screen.getByText('初始化令牌长度校验失败')).toBeTruthy();
  expect(screen.getAllByText('后端出错').length).toBeGreaterThan(0);
  expect(screen.queryByText('无法读取开发状态，将自动重试。')).toBeNull();
  expect(screen.queryByText('正在连接开发管理器')).toBeNull();
});

test.each([
  { generation: 0, phase: 'running' },
  { generation: 1, phase: 'starting' },
  { generation: 1, phase: 'error' },
  { generation: 1, phase: 'stopping' },
  { generation: 1, phase: 'shutdown_blocked' },
])('旧管理器尚无ready字段时保守处理 %j', async (values) => {
  await mount(async () => Response.json(status({ ...values, scheduler_paused: false })));
  expand();
  expect(screen.getByText('开发模式 · 调度状态未知')).toBeTruthy();
  expect(screen.getByRole('button', { name: '调度状态未知' }).hasAttribute('disabled')).toBe(true);
});

test('显式未就绪覆盖非零版本与running状态，不把上次状态当可用后端', async () => {
  await mount(async () => Response.json({ ...status({ scheduler_paused: false }), backend_ready: false }));
  expand();
  expect(screen.getByText('开发模式 · 调度状态未知')).toBeTruthy();
  expect(screen.getByRole('button', { name: '调度状态未知' }).hasAttribute('disabled')).toBe(true);
});

test.each(['building', 'reload_failed', 'error'])('旧后端仍ready时%s保留真实调度状态和编译错误', async (phase) => {
  await mount(async () => Response.json({ ...status({ phase, scheduler_paused: false, last_error: '新代码编译失败' }), backend_ready: true }));
  expand();
  expect(screen.getByText('开发模式 · 调度运行中')).toBeTruthy();
  expect(screen.getByRole('button', { name: '暂停调度' }).hasAttribute('disabled')).toBe(false);
  expect(screen.getByText('新代码编译失败')).toBeTruthy();
});

test('手动开调度等待权威状态，pending 阻止重复提交，不会触发任务运行', async () => {
  let current = status();
  const reply = deferred<Response>();
  const calls: { path: string; init?: RequestInit }[] = [];
  await mount(async (path, init) => {
    calls.push({ path, ...(init ? { init } : {}) });
    return init?.method === 'POST' ? reply.promise : Response.json(current);
  });
  expand();
  const start = await screen.findByRole('button', { name: '开启调度' });
  fireEvent.click(start);
  fireEvent.click(start);
  expect(screen.getByRole('button', { name: '正在提交…' }).hasAttribute('disabled')).toBe(true);
  expect(screen.getByText('开发模式 · 调度已暂停')).toBeTruthy();
  current = status({ scheduler_paused: false });
  await act(async () => { reply.resolve(new Response(null, { status: 204 })); });
  await screen.findByText('开发模式 · 调度运行中');
  const writes = calls.filter((call) => call.init?.method === 'POST');
  expect(writes).toHaveLength(1);
  expect(writes[0]?.path).toBe('/__dev/scheduler');
  expect(JSON.parse(String(writes[0]?.init?.body))).toEqual({ paused: false });
  expect(writes[0]?.init?.credentials).toBe('same-origin');
  expect(calls.every((call) => ['/__dev/status', '/__dev/scheduler'].includes(call.path))).toBe(true);
});

test('控制失败显示提示，保留调度状态并允许重试，不显示原始错误响应', async () => {
  let attempts = 0;
  await mount(async (_path, init) => {
    if (init?.method === 'POST') { attempts++; return new Response('<script>private</script>', { status: 503 }); }
    return Response.json(status());
  });
  expand();
  fireEvent.click(await screen.findByRole('button', { name: '开启调度' }));
  expect(await screen.findByRole('alert')).toHaveProperty('textContent', '控制请求失败，请重试。 响应状态 503');
  expect(document.body.textContent).not.toContain('private');
  expect(screen.getByText('开发模式 · 调度已暂停')).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: '开启调度' }));
  await waitFor(() => expect(attempts).toBe(2));
});

test('后端断连仍可看控制台和最后日志，调度改为未知，轮询恢复后可操作', async () => {
  let online = true;
  let polls = 0;
  const view = await mount(async () => {
    polls++;
    if (!online) throw new Error('secret backend path');
    return Response.json(status({ logs: [{ seq: 1, at: 1794397920000, stream: 'stderr', text: '最近后端错误' }] }));
  }, 'real', 20);
  expand();
  await screen.findByText('最近后端错误');
  online = false;
  await screen.findByText('无法读取开发状态，将自动重试。');
  expect(screen.getByText('开发模式 · 调度状态未知')).toBeTruthy();
  expect(screen.getByRole('button', { name: '调度状态未知' }).hasAttribute('disabled')).toBe(true);
  expect(screen.getByText('最近后端错误')).toBeTruthy();
  expect(document.body.textContent).not.toContain('secret backend path');
  online = true;
  await screen.findByText('开发模式 · 调度已暂停');
  view.unmount();
  const stoppedAt = polls;
  await new Promise((done) => setTimeout(done, 50));
  expect(polls).toBe(stoppedAt);
});

test.each([
  ['building', '正在编译'],
  ['reloading', '新代码已编译，等当前运行结束后切换'],
  ['reload_failed', '新版本启动失败，请查看错误和日志'],
  ['shutdown_blocked', '尚未退出，当前运行结束后再按 Ctrl+C 重试'],
  ['future_phase', '后端状态：future_phase'],
])('接受管理器状态 %s，不把等待或停止受阻误报运行', async (phase, label) => {
  await mount(async () => Response.json(status({ phase })));
  expect((await screen.findAllByText(label)).length).toBeGreaterThan(0);
});

test('编译错误和日志仅作文本渲染，日志限最近100条，毫秒时间按界面语言显示', async () => {
  const malicious = '<img src=x onerror="window.private=true"><script>private()</script>';
  const at = 1794397920000;
  await mount(async () => Response.json(status({ phase: 'building', last_error: malicious,
    logs: Array.from({ length: 105 }, (_, seq) => ({ seq, at, stream: 'stderr', text: `line-${seq} ${malicious}` })),
  })));
  expand();
  await screen.findByText(malicious);
  expect(document.querySelector('img, script')).toBeNull();
  expect(document.querySelectorAll('.kakatta-dev-logs li')).toHaveLength(100);
  expect(screen.queryByText(`line-4 ${malicious}`)).toBeNull();
  expect(screen.getByText(`line-104 ${malicious}`)).toBeTruthy();
  const formatted = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium' }).format(new Date(at));
  expect(screen.getAllByText(`${formatted} · stderr`)).toHaveLength(100);
});

test.each(['normal', 'expired', 'risk', 'missing', 'api_error'] as const)('假数据才有故障控制 %s，只改变下次运行', async (mode) => {
  const calls: { path: string; body: unknown }[] = [];
  let fault = 'normal';
  await mount(async (path, init) => {
    if (init?.method === 'POST') {
      const body = JSON.parse(String(init.body)) as { mode: string };
      calls.push({ path, body }); fault = body.mode;
      return new Response(null, { status: 204 });
    }
    return Response.json(status({ mode: 'fake', fixture: { fault_mode: fault } }));
  }, 'fake');
  expect(screen.getByText('开发模式 · 本机假数据')).toBeTruthy();
  expand();
  expect(await screen.findByText('下次运行生效，不会自动运行任务。')).toBeTruthy();
  const labels = { normal: '恢复正常', expired: '登录失效', risk: '触发验证', missing: '页面缺字段', api_error: '接口错误' };
  fireEvent.click(screen.getByRole('button', { name: labels[mode] }));
  await waitFor(() => expect(screen.getByRole('button', { name: labels[mode] }).getAttribute('aria-pressed')).toBe('true'));
  expect(calls).toEqual([{ path: '/__dev/fault', body: { mode } }]);
});

test('跟随文档语言切换，英文控制、状态和错误文案齐全', async () => {
  await mount(async () => Response.json(status({ phase: 'reloading' })));
  await screen.findByText('开发模式 · 调度已暂停');
  await act(async () => { document.documentElement.lang = 'en'; });
  expect(await screen.findByText('Development mode · Scheduler paused')).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: 'Expand development console' }));
  expect(screen.getByRole('button', { name: 'Start scheduler' })).toBeTruthy();
  expect(screen.getByText('No logs yet')).toBeTruthy();
  expect(screen.getAllByText('New code is built. Waiting for the current run to finish before switching.').length).toBeGreaterThan(0);
});

test('模式不匹配或无效管理器响应不宣称调度已确认', async () => {
  await mount(async () => Response.json(status({ mode: 'fake' })));
  expand();
  await screen.findByText('无法读取开发状态，将自动重试。');
  expect(screen.getByText('开发模式 · 调度状态未知')).toBeTruthy();
});

test('卸载会中止正在进行的状态请求，不保留轮询', async () => {
  let signal: AbortSignal | null | undefined;
  const reply = deferred<Response>();
  const view = await mount(async (_path, init) => { signal = init?.signal; return reply.promise; });
  view.unmount();
  expect(signal?.aborted).toBe(true);
  await act(async () => { reply.resolve(Response.json(status())); });
});

test('慢状态请求完成前不重叠轮询，完成后按间隔继续', async () => {
  const reply = deferred<Response>();
  let polls = 0;
  const view = await mount(async () => {
    polls++;
    return polls === 1 ? reply.promise : Response.json(status());
  }, 'real', 20);
  await act(async () => { await new Promise((done) => setTimeout(done, 60)); });
  expect(polls).toBe(1);
  await act(async () => { reply.resolve(Response.json(status())); });
  await waitFor(() => expect(polls).toBeGreaterThan(1));
  view.unmount();
});

test('控制会取消旧状态请求，晚到旧快照不能把已开启调度改回暂停', async () => {
  const oldReply = deferred<Response>();
  const oldStarted = deferred<void>();
  let polls = 0;
  let oldSignal: AbortSignal | null | undefined;
  let paused = true;
  const view = await mount(async (_path, init) => {
    if (init?.method === 'POST') { paused = false; return new Response(null, { status: 204 }); }
    polls++;
    if (polls === 2) { oldSignal = init?.signal; oldStarted.resolve(); return oldReply.promise; }
    return Response.json(status({ scheduler_paused: paused }));
  }, 'real', 20);
  expand();
  await oldStarted.promise;
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: '开启调度' })); });
  expect(oldSignal?.aborted).toBe(true);
  expect(await screen.findByText('开发模式 · 调度运行中')).toBeTruthy();
  await act(async () => { oldReply.resolve(Response.json(status({ scheduler_paused: true }))); });
  expect(screen.getByText('开发模式 · 调度运行中')).toBeTruthy();
  view.unmount();
});
