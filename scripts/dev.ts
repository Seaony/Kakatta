import { spawn, type ChildProcess } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdir, readFile, readdir } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const imageName = 'kakatta:dev';
const imageLabel = 'io.github.seaony.kakatta.dev-inputs';

export async function developmentImageHash(projectRoot = root) {
  // 管理进程及其依赖编进镜像；普通后端和网页由挂载源码热更新。
  const packages = ['cmd/devhost', 'internal/development', 'internal/devfixture', 'internal/diagnostics', 'internal/xianyu/fake', 'internal/analysis', 'internal/limits', 'internal/locales'];
  const sources = await Promise.all(packages.map(async path => (await readdir(resolve(projectRoot, path), { withFileTypes: true }))
    .filter(file => file.isFile() && (file.name.endsWith('.go') && !file.name.endsWith('_test.go') || file.name.endsWith('.json')))
    .map(file => `${path}/${file.name}`)));
  const inputs = ['Dockerfile', '.dockerignore', 'compose.dev.yaml', 'go.mod', 'go.sum', 'deploy/entrypoint.sh', 'deploy/dev-entrypoint.sh', ...sources.flat()].sort();
  const hash = createHash('sha256');
  for (const path of inputs) hash.update(path).update('\0').update(await readFile(resolve(projectRoot, path))).update('\0');
  return hash.digest('hex');
}

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

function start(command: string[], cwd = root, env: NodeJS.ProcessEnv = process.env, stderr?: (text: string) => void): OwnedProcess {
  const child = spawn(command[0], command.slice(1), { cwd, env, stdio: ['inherit', 'inherit', stderr ? 'pipe' : 'inherit'], detached: true });
  if (stderr) child.stderr?.on('data', chunk => { const text = chunk.toString(); process.stderr.write(text); stderr(text); });
  const exited = new Promise<number>((done, failed) => {
    child.once('error', failed);
    child.once(stderr ? 'close' : 'exit', (code, signal) => done(code ?? (signal ? 1 : 0)));
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

async function waitReady(url: string, child: OwnedProcess, name: string, check: (response: Response) => Promise<boolean> = async () => true) {
  const ended = child.exited.then(() => true);
  const timeout = Date.now() + 120_000;
  while (Date.now() < timeout) {
    if (await Promise.race([ended, Promise.resolve(false)])) throw new Error(`${name}已退出`);
    try {
      const response = await fetch(url, { signal: AbortSignal.timeout(1_000) });
      if (response.status === 200 && await check(response)) return;
    } catch { /* 编译启动时服务可能还没就绪。 */ }
    await Bun.sleep(200);
  }
  throw new Error(`${name}未就绪，请检查上面的日志`);
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

  async function step(command: string[], cwd = projectRoot, env = process.env, stderr?: (text: string) => void) {
    if (stopRequested) throw new Error('开发启动已取消');
    setup = start(command, cwd, env, stderr);
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
      const inputs = await developmentImageHash(projectRoot);
      const inspect = ['docker', 'image', 'ls', '--quiet', '--no-trunc', imageName];
      const previous = await capture(inspect, projectRoot);
      const label = previous ? await capture(['docker', 'image', 'inspect', '--format', `{{ index .Config.Labels "${imageLabel}" }}`, previous], projectRoot) : '';
      if (!previous || label !== inputs) {
        let details = '';
        try {
          await step([...plan.compose, 'build', '--build-arg', `KAKATTA_DEV_INPUT_HASH=${inputs}`, 'dev'], projectRoot, process.env,
            text => { details = (details + text).slice(-32_768); });
        } catch (error) {
          if (stopRequested) throw error;
          let imageState: string;
          try {
            const current = await capture(inspect, projectRoot);
            imageState = previous && current === previous ? '旧镜像仍在，未被覆盖。' : current ? '已有开发镜像仍在。' : '当前没有可用的开发镜像。';
          } catch { imageState = '无法确认旧镜像状态，请检查 Docker 是否仍在运行。'; }
          const pullFailure = /auth\.docker\.io|registry-1\.docker\.io|load metadata|fetch.*token|resolve.*metadata/i.test(details)
            && /EOF|timeout|timed out|no such host|connection|network|TLS handshake/i.test(details);
          const reason = pullFailure ? '拉取基础镜像时网络连接失败。' : '开发镜像构建失败，请查看上方具体错误。';
          const action = pullFailure ? '检查 Docker 网络或代理，恢复后重试 bun run dev:real。' : '修正上方构建错误后重试 bun run dev:real。';
          throw new Error(reason + imageState + action);
        }
      }
      composeStarted = true;
      await step([...plan.compose, 'up', '--detach', '--no-build', 'dev']);
      if (stopRequested) return;
      manager = start([...plan.compose, 'attach', '--sig-proxy=false', '--no-stdin', 'dev'], projectRoot);
    }
    await waitReady('http://127.0.0.1:18081/__dev/status', manager, '开发管理器的后端', async response => (await response.json()).backend_ready === true);
    if (stopRequested) return;
    vite = start(plan.vite, resolve(projectRoot, 'web'), { ...process.env, ...plan.viteEnvironment });
    await waitReady('http://127.0.0.1:5173/', vite, 'Vite 网页服务');
    if (stopRequested) return;
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
