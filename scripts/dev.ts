import { spawn, type ChildProcess } from 'node:child_process';
import { mkdir, readdir } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');

export function developmentPlan(mode: string, projectRoot = root) {
  if (mode !== 'fake' && mode !== 'real') throw new Error('开发模式只能是 fake 或 real');
  const state = resolve(projectRoot, 'build/_dev');
  return {
    mode,
    state,
    data: resolve(state, 'data'),
    tools: resolve(projectRoot, 'build/_tools'),
    manager: resolve(state, 'devhost'),
    compose: ['docker', 'compose', '-f', 'compose.dev.yaml'],
    vite: ['bun', 'run', '--bun', 'vite', '--host', '127.0.0.1', '--port', '5173', '--strictPort'],
    viteEnvironment: {
      KAKATTA_DEVELOPMENT: '1',
      KAKATTA_DEV_MODE: mode,
      KAKATTA_DEV_BACKEND: 'http://127.0.0.1:18080',
      KAKATTA_DEV_CONTROL: 'http://127.0.0.1:18081',
    },
  };
}

export async function needsSeed(data: string) {
  try {
    return (await readdir(data)).length === 0;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === 'ENOENT') return true;
    throw error;
  }
}

type OwnedProcess = { process: ChildProcess; exited: Promise<number> };

function start(command: string[], cwd = root, env: NodeJS.ProcessEnv = process.env): OwnedProcess {
  const child = spawn(command[0], command.slice(1), { cwd, env, stdio: 'inherit', detached: true });
  const exited = new Promise<number>((done, failed) => {
    child.once('error', failed);
    child.once('exit', (code, signal) => done(code ?? (signal ? 1 : 0)));
  });
  return { process: child, exited };
}

async function capture(command: string[], cwd = root) {
  const child = spawn(command[0], command.slice(1), { cwd, stdio: ['ignore', 'pipe', 'inherit'], detached: true });
  let output = '';
  child.stdout.on('data', chunk => { output += chunk.toString(); });
  const code = await new Promise<number>((done, failed) => {
    child.once('error', failed);
    child.once('exit', code => done(code ?? 1));
  });
  if (code !== 0) throw new Error(`命令未成功：${command[0]} ${command[1] ?? ''}`);
  return output.trim();
}

async function waitReady(url: string, manager: OwnedProcess) {
  const ended = manager.exited.then(() => true);
  const timeout = Date.now() + 120_000;
  while (Date.now() < timeout) {
    if (await Promise.race([ended, Promise.resolve(false)])) throw new Error('开发管理器已退出');
    try {
      const response = await fetch(url, { signal: AbortSignal.timeout(1_000) });
      if (response.ok) return;
    } catch { /* 编译启动时控制端口可能还没打开。 */ }
    await Bun.sleep(200);
  }
  throw new Error('开发管理器未就绪，请检查上面的日志');
}

export async function main(mode: string, projectRoot = root) {
  const plan = developmentPlan(mode, projectRoot);
  let setup: OwnedProcess | undefined;
  let manager: OwnedProcess | undefined;
  let vite: OwnedProcess | undefined;
  let composeStarted = false;
  let stopping: Promise<void> | undefined;
  let stopRequested = false;

  async function containerID() {
    return await capture([...plan.compose, 'ps', '--all', '--quiet', 'dev'], projectRoot);
  }

  async function interruptContainer(id?: string) {
    id ??= await containerID();
    if (!id) return '';
    if (await capture(['docker', 'inspect', '--format', '{{.State.Running}}', id], projectRoot) === 'true') {
      try {
        await capture(['docker', 'kill', '--signal', 'SIGINT', id], projectRoot);
      } catch (error) {
        if (await capture(['docker', 'inspect', '--format', '{{.State.Running}}', id], projectRoot) === 'true') throw error;
      }
    }
    return id;
  }

  async function step(command: string[], cwd = projectRoot, env = process.env) {
    if (stopRequested) throw new Error('开发启动已取消');
    setup = start(command, cwd, env);
    const code = await setup.exited;
    setup = undefined;
    if (code !== 0) throw new Error(`命令未成功：${command[0]} ${command[1] ?? ''}`);
  }

  async function stop(retry = false) {
    stopRequested = true;
    if (stopping) {
      if (retry) {
        if (mode === 'real' && composeStarted) await interruptContainer();
        else manager?.process.kill('SIGINT');
      }
      return stopping;
    }
    stopping = (async () => {
      if (mode === 'real' && composeStarted) {
        if (setup) { setup.process.kill('SIGINT'); await setup.exited; }
        const id = await interruptContainer();
        if (id) await capture(['docker', 'wait', id], projectRoot);
        if (manager) await manager.exited;
      } else if (manager) {
        manager.process.kill('SIGINT');
        await manager.exited;
      } else if (setup) {
        setup.process.kill('SIGINT');
        await setup.exited;
      }
      if (vite) {
        vite.process.kill('SIGINT');
        await vite.exited;
      }
      if (composeStarted) {
        const down = start([...plan.compose, 'down', '--remove-orphans'], projectRoot);
        if (await down.exited !== 0) throw new Error('开发容器尚未成功关闭，请检查 Docker 状态');
        composeStarted = false;
      }
    })();
    return stopping;
  }

  const signal = () => { void stop(true).catch(error => console.error(error.message)); };
  process.on('SIGINT', signal);
  process.on('SIGTERM', signal);
  try {
    await mkdir(plan.state, { recursive: true });
    await step(['bun', 'install', '--frozen-lockfile'], resolve(projectRoot, 'web'));
    await step(['bun', 'run', 'build'], resolve(projectRoot, 'web'));
    if (mode === 'fake') {
      await mkdir(plan.tools, { recursive: true });
      const air = resolve(plan.tools, 'air');
      await step(['go', 'install', 'github.com/air-verse/air@v1.67.4'], projectRoot, { ...process.env, GOBIN: plan.tools });
      await step(['go', 'build', '-tags', 'kakatta_dev', '-o', plan.manager, './cmd/devhost']);
      if (await needsSeed(plan.data)) await step(['go', 'run', './cmd/seed', '--data', plan.data]);
      if (stopRequested) return;
      manager = start([plan.manager, '--mode', 'fake', '--root', projectRoot, '--state', plan.state, '--data', plan.data, '--air', air], projectRoot);
    } else {
      composeStarted = true;
      await step([...plan.compose, 'up', '--build', '--detach', 'dev']);
      if (stopRequested) return;
      manager = start([...plan.compose, 'attach', '--sig-proxy=false', '--no-stdin', 'dev'], projectRoot);
    }
    await waitReady('http://127.0.0.1:18081/__dev/status', manager);
    if (stopRequested) return;
    vite = start(plan.vite, resolve(projectRoot, 'web'), { ...process.env, ...plan.viteEnvironment });
    console.log('开发网页：http://127.0.0.1:5173；按 Ctrl+C 安全退出');
    const finished = await Promise.race([manager.exited, vite.exited]);
    if (finished !== 0 && !stopRequested) throw new Error('开发进程已退出，请检查上面的日志');
  } catch (error) {
    if (!stopRequested) throw error;
  } finally {
    await stop();
    process.off('SIGINT', signal);
    process.off('SIGTERM', signal);
  }
}

if (import.meta.main) {
  main(process.argv[2] ?? 'fake').catch(error => {
    console.error(error.message);
    process.exitCode = 1;
  });
}
