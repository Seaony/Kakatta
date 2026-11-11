import { createConnection, createServer, type Socket } from 'node:net';
import { mkdir, mkdtemp, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';

export async function earlyViteRequest(viteEntry = resolve(import.meta.dir, '../web/node_modules/vite/bin/vite.js')) {
  const project = resolve(import.meta.dir, '..');
  const output = resolve(project, 'build/_dev-startup');
  await mkdir(output, { recursive: true });
  const directory = await mkdtemp(resolve(output, 'first-request-'));
  const reservation = createServer();
  await new Promise<void>((done, failed) => { reservation.once('error', failed); reservation.listen(0, '127.0.0.1', done); });
  const port = (reservation.address() as { port: number }).port;
  await new Promise<void>((done, failed) => reservation.close(error => error ? failed(error) : done()));
  await writeFile(resolve(directory, 'index.html'), '<!doctype html><h1>startup-ok</h1>');
  const configuration = resolve(directory, 'vite.config.mjs');
  await writeFile(configuration, `export default ${JSON.stringify({ root: directory, cacheDir: resolve(directory, 'cache'), server: { host: '127.0.0.1', port, strictPort: true } })};`);

  const connections: { socket: Socket; received: string }[] = [];
  let stopped = false;
  const connect = async () => {
    while (!stopped) {
      const socket = createConnection({ host: '127.0.0.1', port });
      const connected = await new Promise<boolean>(done => {
        socket.once('connect', () => done(true));
        socket.once('error', () => done(false));
      });
      if (!connected) { socket.destroy(); await Bun.sleep(1); continue; }
      const connection = { socket, received: '' };
      connections.push(connection);
      socket.on('data', data => { connection.received += data.toString(); });
      socket.write(`GET / HTTP/1.1\r\nHost: 127.0.0.1:${port}\r\nConnection: keep-alive\r\n\r\n`);
      return;
    }
  };
  const clients = Array.from({ length: 8 }, connect);
  const server = Bun.spawn([process.execPath, viteEntry, directory, '--config', configuration, '--host', '127.0.0.1', '--port', String(port), '--strictPort'], { cwd: resolve(project, 'web'), stdout: 'pipe', stderr: 'pipe' });
  const stdout = new Response(server.stdout).text(), stderr = new Response(server.stderr).text();
  const started = Date.now();
  let readyWithoutDisconnect = false;
  try {
    const deadline = Date.now() + 5_000;
    while (Date.now() < deadline && server.exitCode === null) {
      if (connections.length === 8 && connections.every(connection => /^HTTP\/1\.1 200/.test(connection.received) && connection.received.includes('startup-ok'))) {
        readyWithoutDisconnect = true;
        break;
      }
      await Bun.sleep(10);
    }
    return { readyWithoutDisconnect, elapsed: Date.now() - started, port, directory, connections: connections.map(connection => ({ received: connection.received.slice(0, 200) })) };
  } finally {
    stopped = true;
    for (const connection of connections) connection.socket.destroy();
    await Promise.all(clients);
    // 先释放首连接，让旧版本也能走完探测，再收掉本测试创建的进程。
    await Bun.sleep(50);
    server.kill('SIGINT');
    await Promise.race([server.exited, Bun.sleep(2000)]);
    if (server.exitCode === null) server.kill('SIGKILL');
    await server.exited;
    await writeFile(resolve(directory, 'stdout.log'), await stdout);
    await writeFile(resolve(directory, 'stderr.log'), await stderr);
  }
}

export async function occupiedVitePort() {
  const project = resolve(import.meta.dir, '..');
  await mkdir(resolve(project, 'build/_dev-startup'), { recursive: true });
  const directory = await mkdtemp(resolve(project, 'build/_dev-startup/occupied-'));
  const owner = createServer(socket => socket.end('原有服务'));
  await new Promise<void>((done, failed) => {
    owner.once('error', failed);
    owner.listen(0, '127.0.0.1', done);
  });
  const port = (owner.address() as { port: number }).port;
  await writeFile(resolve(directory, 'index.html'), '<!doctype html><h1>occupied-test</h1>');
  const config = resolve(directory, 'vite.config.mjs');
  await writeFile(config, `export default ${JSON.stringify({ root: directory, cacheDir: resolve(directory, 'cache') })};`);
  const server = Bun.spawn([process.execPath, resolve(project, 'web/node_modules/vite/bin/vite.js'), directory, '--config', config, '--host', '127.0.0.1', '--port', String(port), '--strictPort'], { stdout: 'pipe', stderr: 'pipe' });
  const stdout = new Response(server.stdout).text(), stderr = new Response(server.stderr).text();
  try {
    await Promise.race([server.exited, Bun.sleep(5000)]);
    if (server.exitCode === null) throw new Error('占用端口时Vite没有退出');
    const result = { exitCode: server.exitCode, stderr: await stderr, ownerStillListening: owner.listening };
    await writeFile(resolve(directory, 'result.json'), JSON.stringify(result, null, 2));
    return result;
  } finally {
    if (server.exitCode === null) server.kill('SIGINT');
    await server.exited;
    await stdout;
    await stderr;
    await new Promise<void>(done => owner.close(() => done()));
  }
}
