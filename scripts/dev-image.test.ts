import { expect, test } from 'bun:test';
import { chmod, cp, mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { relative, resolve } from 'node:path';
import { developmentImageHash } from './dev';

const repository = resolve(import.meta.dir, '..');
const directories = ['cmd/devhost', 'internal/development', 'internal/devfixture', 'internal/diagnostics', 'internal/xianyu/fake', 'internal/analysis', 'internal/limits', 'internal/locales'];
const files = ['Dockerfile', '.dockerignore', 'compose.dev.yaml', 'go.mod', 'go.sum', 'deploy/entrypoint.sh', 'deploy/dev-entrypoint.sh'];

async function fixture(label = 'match', failure = '') {
  await mkdir(resolve(repository, 'build/_round2/block1'), { recursive: true });
  const dir = await mkdtemp(resolve(repository, 'build/_round2/block1/commands-'));
  await mkdir(resolve(dir, 'bin')); await mkdir(resolve(dir, 'web'));
  for (const path of [...files, ...directories]) {
    await mkdir(resolve(dir, path, '..'), { recursive: true });
    await cp(resolve(repository, path), resolve(dir, path), { recursive: true });
  }
  if (label !== 'missing') await writeFile(resolve(dir, 'image'), 'sha256:previous');
  await writeFile(resolve(dir, 'label'), label); await writeFile(resolve(dir, 'failure'), failure);
  const command = resolve(dir, 'command.ts'), events = resolve(dir, 'events');
  await writeFile(command, `
    import { appendFileSync, existsSync, readFileSync, writeFileSync, rmSync } from 'node:fs';
    import { basename, resolve } from 'node:path';
    import * as development from ${JSON.stringify(resolve(repository, 'scripts/dev.ts'))};
    const root=${JSON.stringify(dir)}, args=process.argv.slice(2), name=basename(process.argv[1]);
    const file=(name)=>resolve(root,name), read=(name)=>existsSync(file(name))?readFileSync(file(name),'utf8'):'';
    const event=(text)=>appendFileSync(file('events'),text+'\\n');
    if(name==='bun') {
      if(args.includes('vite')) {writeFileSync(file('vite-pid'),String(process.pid));process.on('SIGINT',()=>process.exit(0));setInterval(()=>{},1000)}
    } else if(args[0]==='image' && args[1]==='ls') console.log(read('image'));
    else if(args[0]==='image' && args[1]==='inspect') console.log(read('label')==='match'?await development.developmentImageHash(root):read('label'));
    else if(args.includes('build') || args.includes('--build')) {
      event('build');
      if(read('failure')) {console.error(read('failure'));process.exit(1)}
      writeFileSync(file('image'),'sha256:rebuilt');
      const value=args.find(x=>x.startsWith('KAKATTA_DEV_INPUT_HASH='));
      writeFileSync(file('label'),value?.split('=')[1]??'old-build');
      if(args.includes('up')) {writeFileSync(file('running'),'true');event('up '+args.join(' '))}
    } else if(args.includes('up')) {
      writeFileSync(file('running'),'true');rmSync(file('manager-pid'),{force:true});rmSync(file('vite-pid'),{force:true});event('up '+args.join(' '));
    } else if(args.includes('attach')) {
      writeFileSync(file('manager-pid'),String(process.pid));
      process.on('SIGINT',()=>{writeFileSync(file('running'),'false');process.exit(0)});setInterval(()=>{},1000);
    } else if(args.includes('ps')) console.log('fixture-container');
    else if(args[0]==='inspect') console.log(read('running'));
    else if(args[0]==='kill') process.kill(Number(read('manager-pid')),'SIGINT');
    else if(args[0]==='wait') console.log('0');
    else if(args.includes('down')) event('down');
  `);
  for (const name of ['docker', 'bun']) {
    const path = resolve(dir, 'bin', name);
    await writeFile(path, `#!${process.execPath}\nimport ${JSON.stringify(command)};\n`); await chmod(path, 0o700);
  }
  const launch = resolve(dir, 'launch.ts');
  await writeFile(launch, `
    import {existsSync,appendFileSync} from 'node:fs';
    import {main} from ${JSON.stringify(resolve(repository, 'scripts/dev.ts'))};
    globalThis.fetch=async(url)=>new Response(JSON.stringify({backend_ready:existsSync(${JSON.stringify(resolve(dir, 'manager-pid'))})}),{status:String(url).includes('18081')||existsSync(${JSON.stringify(resolve(dir, 'vite-pid'))})?200:503});
    const log=console.log;console.log=(...args)=>{if(String(args[0]).includes('开发网页'))appendFileSync(${JSON.stringify(events)},'advertised\\n');log(...args)};
    try {await main('real',${JSON.stringify(dir)})} catch(error) {console.error(error.message);process.exitCode=1}
  `);
  const lines = async () => (await readFile(events, 'utf8').catch(() => '')).trim().split('\n');
  return { dir, lines, async run(success = true) {
    const before = (await lines()).filter(line => line === 'advertised').length;
    const child = Bun.spawn([process.execPath, launch], { cwd: dir, env: { ...process.env, PATH: `${resolve(dir, 'bin')}:${process.env.PATH}` }, stdout: 'pipe', stderr: 'pipe' });
    const completed = Promise.all([child.exited, new Response(child.stdout).text(), new Response(child.stderr).text()]);
    try {
      if (success) {
        const deadline = Date.now() + 5_000;
        while ((await lines()).filter(line => line === 'advertised').length === before) {
          if (child.exitCode !== null || Date.now() > deadline) throw new Error('开发进程没有就绪');
          await Bun.sleep(10);
        }
        child.kill('SIGINT');
      }
      return await completed;
    } finally { if (child.exitCode === null) { child.kill('SIGINT'); await child.exited; } }
  }, async close() { await rm(dir, { recursive: true, force: true }); } };
}

test('已有输入一致的镜像连续启动不构建，也不让compose隐式构建', async () => {
  const f = await fixture();
  try {
    expect((await f.run())[0]).toBe(0); expect((await f.run())[0]).toBe(0);
    const lines = await f.lines();
    expect(lines.filter(line => line === 'build')).toHaveLength(0);
    expect(lines.filter(line => line.startsWith('up ')).every(line => !line.includes('--build') && line.includes('--no-build'))).toBe(true);
  } finally { await f.close(); }
}, 15_000);

test.each(['missing', 'changed'])('镜像%s时只构建一次，下一次复用，构建输入变化才重建', async (label) => {
  const f = await fixture(label);
  try {
    expect((await f.run())[0]).toBe(0); expect((await f.run())[0]).toBe(0);
    expect((await f.lines()).filter(line => line === 'build')).toHaveLength(1);
    await writeFile(resolve(f.dir, 'go.mod'), (await readFile(resolve(f.dir, 'go.mod'), 'utf8')) + '\n');
    expect((await f.run())[0]).toBe(0);
    expect((await f.lines()).filter(line => line === 'build')).toHaveLength(2);
  } finally { await f.close(); }
}, 15_000);

test('拉镜像网络失败时说明原因和旧镜像状态，不清理未启动的容器', async () => {
  const f = await fixture('changed', 'failed to fetch oauth token: https://auth.docker.io/token: EOF');
  try {
    const [code, , error] = await f.run(false);
    expect(code).not.toBe(0); expect(error).toContain('拉取基础镜像'); expect(error).toContain('网络');
    expect(error).toContain('旧镜像仍在'); expect(error).toContain('bun run dev:real');
    expect((await f.lines()).includes('down')).toBe(false);
    expect(await readFile(resolve(f.dir, 'image'), 'utf8')).toBe('sha256:previous');
  } finally { await f.close(); }
});

test('散列覆盖管理器实际编译的源码和资源，普通后端网页及测试改动不用重建', async () => {
  const f = await fixture();
  try {
    const command = Bun.spawn(['go', 'list', '-tags', 'kakatta_dev', '-deps', '-f', '{{if .Module}}{{if .Module.Main}}{{.Dir}}{{range .GoFiles}}|{{.}}{{end}}{{range .EmbedFiles}}|{{.}}{{end}}{{end}}{{end}}', './cmd/devhost'], { cwd: repository, env: { ...process.env, GOOS: 'linux', GOPROXY: 'off' }, stdout: 'pipe', stderr: 'pipe' });
    const [code, output, error] = await Promise.all([command.exited, new Response(command.stdout).text(), new Response(command.stderr).text()]);
    expect(code, error).toBe(0);
    const compiled = output.trim().split('\n').filter(Boolean).flatMap(line => {
      const [directory, ...names] = line.split('|');
      return names.map(name => relative(repository, resolve(directory!, name)));
    });
    expect(compiled.length).toBeGreaterThan(0);
    const before = await developmentImageHash(f.dir);
    for (const path of [...files, ...compiled]) {
      const file = resolve(f.dir, path), original = await readFile(file, 'utf8');
      await writeFile(file, original + '\n');
      expect(await developmentImageHash(f.dir), path).not.toBe(before);
      await writeFile(file, original);
    }
    for (const path of ['internal/service/hot.go', 'web/hot.tsx', 'internal/development/ignored_test.go']) {
      const file = resolve(f.dir, path); await mkdir(resolve(file, '..'), { recursive: true }); await writeFile(file, '热更新或测试内容');
      expect(await developmentImageHash(f.dir), path).toBe(before);
    }
  } finally { await f.close(); }
}, 15_000);
