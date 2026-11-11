import assert from 'node:assert/strict';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const output = fileURLToPath(new URL('../../../build/_dia1/live-events/', import.meta.url));
await mkdir(output, { recursive: true });
const built = await Bun.build({ entrypoints: [fileURLToPath(new URL('./live-events-browser.fixture.tsx', import.meta.url))], target: 'browser', minify: false });
assert(built.success, built.logs.map(String).join('\n'));
const bundle = await built.outputs[0]!.text();
let revision = 0, streamID = 0;
const streams = new Map<number, ReadableStreamDefaultController<Uint8Array>>();
const evidence: Record<string, any> = { navigation: [], lifecycles: [], console_errors: [], http_errors: [], external_requests: [], sse: [] };
const encoder = new TextEncoder();
const server = Bun.serve({ hostname: '127.0.0.1', port: 0, idleTimeout: 0, fetch(request) {
  const path = new URL(request.url).pathname;
  if (path === '/api/v1/events') {
    const id = ++streamID;
    return new Response(new ReadableStream<Uint8Array>({
      start(controller) { streams.set(id, controller); evidence.sse.push({ kind: 'opened', id, at: Date.now() }); controller.enqueue(encoder.encode(': ready\n\n')); },
      cancel() { streams.delete(id); evidence.sse.push({ kind: 'closed', id, at: Date.now() }); },
    }), { headers: { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' } });
  }
  if (path === '/api/v1/settings') return Response.json({ values: { 'probe.revision': revision }, sources: {}, warnings: [] });
  if (path === '/client.js') return new Response(bundle, { headers: { 'Content-Type': 'text/javascript' } });
  if (path === '/favicon.ico') return new Response(null, { status: 204 });
  if (['/one', '/two', '/three'].includes(path)) return new Response('<!doctype html><html><head><meta charset="utf-8"><title>事件流回归</title></head><body><div id="root"></div><script type="module" src="/client.js"></script></body></html>', { headers: { 'Content-Type': 'text/html' } });
  return new Response(null, { status: 404 });
} });
const profile = await mkdtemp(join(tmpdir(), 'kakatta-live-events-'));
const child = Bun.spawn([process.env.KAKATTA_LIVE_EVENTS_CHROME!, '--headless=new', '--no-first-run', '--no-default-browser-check',
  '--disable-background-networking', '--disable-component-update', '--disable-sync', '--disable-default-apps', '--disable-breakpad',
  '--remote-debugging-port=0', `--user-data-dir=${profile}`,
  '--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1', 'about:blank'], { stdout: 'ignore', stderr: 'ignore' });
let socket: WebSocket | undefined, session: string | undefined, sequence = 0;
const pending = new Map<number, { resolve(value: any): void; reject(error: Error): void }>();
function send(method: string, params: Record<string, unknown> = {}, target = session): Promise<any> {
  const id = ++sequence;
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => { pending.delete(id); reject(new Error('浏览器命令超时：' + method)); }, 10_000);
    pending.set(id, { resolve: (value) => { clearTimeout(timer); resolve(value); }, reject: (error) => { clearTimeout(timer); reject(error); } });
    socket!.send(JSON.stringify({ id, method, params, ...(target ? { sessionId: target } : {}) }));
  });
}
async function evaluate(expression: string): Promise<any> {
  const result = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
  if (result.exceptionDetails) throw new Error(result.exceptionDetails.exception?.description ?? result.exceptionDetails.text);
  return result.result.value;
}
async function until<T>(read: () => Promise<T>, accept: (value: T) => boolean): Promise<T> {
  const deadline = Date.now() + 5_000;
  while (Date.now() < deadline) { const value = await read(); if (accept(value)) return value; await Bun.sleep(25); }
  throw new Error('等待页面状态超时');
}
const state = () => evaluate(`({path:location.pathname,revision:document.querySelector('#revision')?.textContent,draft:document.querySelector('#draft')?.value,...window.liveEventsProbe})`);
async function ready(path: string) {
  return until(state, (value) => value.path === path && value.revision === String(revision));
}
async function writeDraft(value: string) {
  const point = await evaluate(`(()=>{const r=document.querySelector('#draft').getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2}})()`);
  for (const type of ['mousePressed', 'mouseReleased']) await send('Input.dispatchMouseEvent', { type, ...point, button: 'left', clickCount: 1 });
  await send('Input.insertText', { text: value });
}
async function changeRevision(next: number) {
  revision = next;
  for (const controller of streams.values()) controller.enqueue(encoder.encode('event: settings.changed\ndata: {}\n\n'));
  await until(state, (value) => value.revision === String(next));
}
try {
  const portFile = await until(async () => readFile(join(profile, 'DevToolsActivePort'), 'utf8').catch(() => ''), Boolean);
  const [port, path] = portFile.trim().split('\n');
  socket = new WebSocket(`ws://127.0.0.1:${port}${path}`);
  await new Promise<void>((resolve, reject) => { socket!.onopen = () => resolve(); socket!.onerror = () => reject(new Error('临时浏览器连接失败')); });
  socket.onmessage = (event) => {
    const message = JSON.parse(String(event.data));
    if (message.id) {
      const command = pending.get(message.id); pending.delete(message.id);
      message.error ? command?.reject(new Error(message.error.message)) : command?.resolve(message.result);
    } else {
      if (message.method === 'Runtime.exceptionThrown') evidence.console_errors.push(message.params.exceptionDetails.exception?.description ?? message.params.exceptionDetails.text);
      if (message.method === 'Runtime.consoleAPICalled' && message.params.type === 'error') evidence.console_errors.push(message.params.args.map((value: any) => value.value ?? value.description).join(' '));
      if (message.method === 'Network.requestWillBeSent') {
        const request = new URL(message.params.request.url);
        if (['http:', 'https:'].includes(request.protocol) && request.origin !== server.url.origin) evidence.external_requests.push(request.origin);
      }
      if (message.method === 'Network.responseReceived' && message.params.response.status >= 400) evidence.http_errors.push({ path: new URL(message.params.response.url).pathname, status: message.params.response.status });
    }
  };
  const target = await send('Target.createTarget', { url: 'about:blank' }, undefined);
  session = (await send('Target.attachToTarget', { targetId: target.targetId, flatten: true }, undefined)).sessionId;
  for (const method of ['Runtime.enable', 'Page.enable', 'Network.enable']) await send(method);
  await send('Emulation.setDeviceMetricsOverride', { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
  for (let index = 0; index < 12; index++) {
    const route = ['/one', '/two', '/three'][index % 3]!;
    await send('Page.navigate', { url: new URL(route, server.url).href });
    const value = await ready(route);
    await until(async () => streams.size, (count) => count > 0);
    if (index >= 10) await writeDraft(index === 10 ? 'back draft' : 'forward draft');
    evidence.navigation.push({ index, ...value, active_streams: streams.size });
  }
  assert(evidence.navigation.every((value: any) => value.active_streams === 1), '旧文档仍占着事件连接');
  const history = await send('Page.getNavigationHistory');
  const current = history.currentIndex, backDocument = evidence.navigation[10], forwardDocument = evidence.navigation[11];
  await changeRevision(1);
  await send('Page.navigateToHistoryEntry', { entryId: history.entries[current - 1].id });
  const back = await ready('/two');
  assert.equal(back.documentID, backDocument.documentID); assert.equal(back.persisted, true); assert.equal(back.draft, 'back draft');
  await changeRevision(2);
  evidence.lifecycles.push({ kind: 'back', ...await state(), active_streams: streams.size });
  await send('Page.navigateToHistoryEntry', { entryId: history.entries[current].id });
  const forward = await ready('/three');
  assert.equal(forward.documentID, forwardDocument.documentID); assert.equal(forward.persisted, true); assert.equal(forward.draft, 'forward draft');
  await changeRevision(3);
  evidence.lifecycles.push({ kind: 'forward', ...await state(), active_streams: streams.size });
  assert(evidence.lifecycles.every((value: any) => value.active_streams === 1));
  assert.equal(evidence.console_errors.length, 0); assert.equal(evidence.http_errors.length, 0); assert.equal(evidence.external_requests.length, 0);
  evidence.passed = true;
} catch (error) {
  evidence.error = String(error);
  if (socket && session) {
    const screenshot = await send('Page.captureScreenshot', { format: 'png' }).catch(() => null);
    if (screenshot) await writeFile(join(output, 'browser-failure.png'), Buffer.from(screenshot.data, 'base64'));
  }
} finally {
  evidence.streams_before_browser_close = streams.size;
  if (socket?.readyState === WebSocket.OPEN) await send('Browser.close', {}, undefined).catch(() => {});
  socket?.close();
  await Promise.race([child.exited, Bun.sleep(1_500)]);
  if (child.exitCode === null) child.kill('SIGKILL');
  await child.exited;
  evidence.streams_after_browser_close = streams.size;
  if (streams.size !== 0) { evidence.passed = false; evidence.error = '浏览器关闭后仍留有事件连接'; }
  await rm(profile, { recursive: true, force: true });
  await server.stop(true);
  evidence.browser_closed = true; evidence.profile_removed = true; evidence.server_closed = true;
  await writeFile(join(output, 'browser-evidence.json'), JSON.stringify(evidence, null, 2));
}
console.log(JSON.stringify(evidence));
if (!evidence.passed) process.exitCode = 1;
