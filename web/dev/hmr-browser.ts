import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm, writeFile, mkdir } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const address = process.env.KAKATTA_HMR_TEST_URL;
const chrome = process.env.KAKATTA_HMR_CHROME ?? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
type Event = { method: string; params: Record<string, any>; sessionId?: string };

// 只在已有本机开发服务上验收，普通测试不启动开发服务。
async function run() {
  const url = new URL(address!);
  assert(['127.0.0.1', 'localhost'].includes(url.hostname));
  const web = fileURLToPath(new URL('../', import.meta.url));
  const source = join(web, 'dev/messages.ts');
  const before = (await readFile(source, 'utf8')).replace('Local fake data · HMR check', 'Local fake data');
  const profile = await mkdtemp(join(tmpdir(), 'kakatta-hmr-chrome-'));
  const output = join(web, '../build/_dev-acceptance/hmr');
  await mkdir(output, { recursive: true });
  const events: Event[] = [];
  const pending = new Map<number, { resolve(value: any): void; reject(error: Error): void }>();
  let seq = 0;
  let socket: WebSocket | undefined;
  let session: string | undefined;
  const child = Bun.spawn([chrome, '--headless=new', '--no-first-run', '--no-default-browser-check',
    '--disable-background-networking', '--disable-component-update', '--disable-sync', '--disable-default-apps',
    '--disable-breakpad', '--remote-debugging-port=0', `--user-data-dir=${profile}`,
    '--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1', 'about:blank'],
  { stdout: 'ignore', stderr: 'ignore' });
  async function waitFor<T>(read: () => Promise<T>, accept: (value: T) => boolean, timeout = 10_000): Promise<T> {
    const until = Date.now() + timeout;
    while (Date.now() < until) {
      const value = await read();
      if (accept(value)) return value;
      await Bun.sleep(75);
    }
    throw new Error('等待热更状态超时');
  }
  function send(method: string, params: Record<string, unknown> = {}, target = session): Promise<any> {
    const id = ++seq;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { pending.delete(id); reject(new Error(`浏览器命令超时：${method}`)); }, 10_000);
      pending.set(id, { resolve: (value) => { clearTimeout(timer); resolve(value); }, reject: (error) => { clearTimeout(timer); reject(error); } });
      socket!.send(JSON.stringify({ id, method, params, ...(target ? { sessionId: target } : {}) }));
    });
  }
  const evaluate = async (expression: string) => (await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true })).result.value;
  async function snapshot(name: string) {
    const state = await evaluate(`({text:document.querySelector('#kakatta-development-console')?.textContent,language:document.documentElement.lang,marker:window.__kakattaHmrProbe,roots:document.querySelectorAll('#kakatta-development-console').length,url:location.pathname})`);
    await writeFile(join(output, `${name}.json`), JSON.stringify(state, null, 2));
    const image = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
    await writeFile(join(output, `${name}.png`), Buffer.from(image.data, 'base64'));
    return state;
  }
  try {
    await writeFile(source, before);
    const portFile = await waitFor(async () => readFile(join(profile, 'DevToolsActivePort'), 'utf8').catch(() => ''), Boolean);
    const [port, path] = portFile.trim().split('\n');
    socket = new WebSocket(`ws://127.0.0.1:${port}${path}`);
    await new Promise<void>((resolve, reject) => { socket!.onopen = () => resolve(); socket!.onerror = () => reject(new Error('无法连接临时浏览器')); });
    socket.onmessage = (event) => {
      const data = JSON.parse(String(event.data));
      if (data.id) {
        const command = pending.get(data.id); pending.delete(data.id);
        if (data.error) command?.reject(new Error(data.error.message)); else command?.resolve(data.result);
      } else {
        if (data.method === 'Fetch.requestPaused') {
          const requested = new URL(data.params.request.url);
          const allowed = ['127.0.0.1', 'localhost', '[::1]'].includes(requested.hostname);
          void send(allowed ? 'Fetch.continueRequest' : 'Fetch.failRequest', {
            requestId: data.params.requestId, ...(!allowed ? { errorReason: 'BlockedByClient' } : {}),
          }).catch(() => {});
        }
        if (['Runtime.consoleAPICalled', 'Runtime.exceptionThrown', 'Network.webSocketCreated',
          'Network.webSocketFrameReceived', 'Page.frameNavigated'].includes(data.method)) events.push(data);
      }
    };
    const target = await send('Target.createTarget', { url: 'about:blank' }, undefined);
    session = (await send('Target.attachToTarget', { targetId: target.targetId, flatten: true }, undefined)).sessionId;
    for (const method of ['Runtime.enable', 'Page.enable', 'Network.enable', 'Fetch.enable']) await send(method);
    await send('Emulation.setDeviceMetricsOverride', { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
    await send('Page.navigate', { url: url.href });
    await waitFor(() => evaluate(`Boolean(document.querySelector('#kakatta-development-console'))`), Boolean);
    await evaluate(`document.documentElement.lang='en'; window.__kakattaHmrProbe='preserved'`);
    await waitFor(() => evaluate(`document.documentElement.lang='en'; document.querySelector('#kakatta-development-console')?.textContent`), (text) => text.includes('Local fake data'));
    const initial = await snapshot('before');
    assert(!initial.text.includes('HMR check'));
    await writeFile(source, before.replace('Local fake data', 'Local fake data · HMR check'));
    await waitFor(() => evaluate(`document.querySelector('#kakatta-development-console')?.textContent`), (text) => text?.includes('HMR check'), 12_000);
    const updated = await snapshot('updated');
    assert.equal(updated.marker, 'preserved');
    assert.equal(updated.roots, 1);
    await writeFile(source, before);
    await waitFor(() => evaluate(`document.querySelector('#kakatta-development-console')?.textContent`), (text) => text?.includes('Local fake data') && !text.includes('HMR check'));
    const restored = await snapshot('restored');
    assert.equal(restored.marker, 'preserved');
    assert.equal(restored.roots, 1);
  } finally {
    if (socket && session) await snapshot('final').catch(() => {});
    await writeFile(source, before);
    const diagnostics = events.map(({ method, params }) => {
      if (method === 'Network.webSocketCreated') return { method, url: String(params.url).split('?')[0] };
      if (method === 'Network.webSocketFrameReceived') {
        try {
          const message = JSON.parse(params.response.payloadData);
          return { method, type: message.type, updates: message.updates?.map((update: any) => ({ type: update.type, path: update.path, acceptedPath: update.acceptedPath })) };
        } catch { return { method }; }
      }
      if (method === 'Runtime.consoleAPICalled') return { method, type: params.type, text: params.args.map((arg: any) => arg.value ?? arg.description ?? '').join(' ').slice(0, 4000) };
      if (method === 'Runtime.exceptionThrown') return { method, text: params.exceptionDetails.exception?.description ?? params.exceptionDetails.text };
      return { method, url: String(params.frame.url).split('?')[0], parent: Boolean(params.frame.parentId) };
    });
    await writeFile(join(output, 'events.json'), JSON.stringify(diagnostics, null, 2));
    if (socket?.readyState === WebSocket.OPEN) await send('Browser.close', {}, undefined).catch(() => {});
    socket?.close();
    await Promise.race([child.exited, Bun.sleep(1500)]);
    if (child.exitCode === null) child.kill('SIGKILL');
    await child.exited;
    await rm(profile, { recursive: true, force: true });
  }
}

if (import.meta.main) await run();
