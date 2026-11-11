import { describe, expect, test } from 'bun:test';
import { chmod, mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { resolve } from 'node:path';
import { developmentPlan, needsSeed } from './dev.ts';
import { earlyViteRequest, occupiedVitePort } from './dev-startup.fixture.ts';

describe('根开发命令', () => {
  test('首批HTTP连接保持打开时也能完成启动并收到页面', async () => {
    const result = await earlyViteRequest();
    await writeFile(resolve(result.directory, 'result.json'), JSON.stringify(result, null, 2));
    expect(result.readyWithoutDisconnect, JSON.stringify(result)).toBe(true);
  }, 15_000);

  test('固定端口已占用时退出且不关闭原监听', async () => {
    const result = await occupiedVitePort();
    expect(result.exitCode).not.toBe(0);
    expect(result.stderr).toContain('already in use');
    expect(result.ownerStillListening).toBe(true);
  }, 15_000);
  test('假模式使用独立开发目录和固定本地代理', () => {
    const plan = developmentPlan('fake', '/project');
    expect(plan.data).toBe('/project/build/_dev/data');
    expect(plan.tools).toBe('/project/build/_tools');
    expect(plan.viteEnvironment.KAKATTA_DEV_MODE).toBe('fake');
    expect(plan.vite).toEqual(['bun', 'run', '--bun', 'vite', '--host', '127.0.0.1', '--port', '5173', '--strictPort']);
  });

  test('真实模式使用独立 compose 文件且不接受未知模式', () => {
    expect(developmentPlan('real').compose).toEqual(['docker', 'compose', '-f', 'compose.dev.yaml']);
    expect(() => developmentPlan('other')).toThrow('fake 或 real');
  });

  test('只给不存在或首次空目录准备样例，已有文件绝不覆盖', async () => {
    const dir = await mkdtemp(resolve(tmpdir(), 'kakatta-dev-seed-'));
    try {
      const data = resolve(dir, 'data');
      expect(await needsSeed(data)).toBe(true);
      await mkdir(data);
      expect(await needsSeed(data)).toBe(true);
      await writeFile(resolve(data, 'existing.db'), '保留');
      expect(await needsSeed(data)).toBe(false);
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  });

  test('一次退出先收管理器再停网页，第二次启动保留已有样例', async () => {
    const dir = await mkdtemp(resolve(tmpdir(), 'kakatta-dev-processes-'));
    const bin = resolve(dir, 'bin');
    const events = resolve(dir, 'events');
    const fixture = resolve(dir, 'fake-command.ts');
    const launcher = resolve(dir, 'launch.ts');
    await mkdir(bin);
    await mkdir(resolve(dir, 'web'));
    const runner = `import {main} from ${JSON.stringify(resolve(import.meta.dir, 'dev.ts'))};
      import {appendFileSync} from 'node:fs';
      let managerChecks=0,viteChecks=0;
      const event=(line)=>appendFileSync(${JSON.stringify(events)},line+'\\n');
      globalThis.fetch=async(url)=>{
        if(String(url).includes('18081')){
          const ready=++managerChecks>1;if(ready)event('backend-ready');
          return new Response(JSON.stringify({backend_ready:ready}),{status:200});
        }
        const ready=++viteChecks>1;if(ready)event('vite-ready');
        return new Response('测试网页',{status:ready?200:503});
      };
      const log=console.log;console.log=(...args)=>{if(String(args[0]).includes('开发网页'))event('advertised');log(...args)};
      await main('fake',${JSON.stringify(dir)});`;
    await writeFile(launcher, runner);
    await writeFile(fixture, `
      import {appendFileSync,mkdirSync,writeFileSync,chmodSync} from 'node:fs';
      import {basename,dirname,resolve} from 'node:path';
      const args=process.argv.slice(2), command=basename(process.argv[1]);
      const event=(line)=>appendFileSync(${JSON.stringify(events)},line+'\\n');
      const managed=(label)=>{event('start '+label);process.on('SIGINT',()=>{event('stop '+label);process.exit(0)});setInterval(()=>{},1000)};
      if(command==='go'){
        if(args[0]==='build'){
          const file=args[args.indexOf('-o')+1];
          writeFileSync(file,${JSON.stringify(`#!${process.execPath}\nimport ${JSON.stringify(fixture)};\n`)});
          chmodSync(file,0o700);
        }else if(args[0]==='run'){
          const data=args[args.indexOf('--data')+1];mkdirSync(data,{recursive:true});writeFileSync(resolve(data,'example.db'),'保留');event('seed');
        }
      }else if(command==='bun'){
        if(args.includes('vite'))managed('vite');
      }else managed('manager');
    `);
    for (const command of ['go', 'bun']) {
      const path = resolve(bin, command);
      await writeFile(path, `#!${process.execPath}\nimport ${JSON.stringify(fixture)};\n`);
      await chmod(path, 0o700);
    }
    const text = async () => { try { return await readFile(events, 'utf8'); } catch { return ''; } };
    try {
      for (let run = 0; run < 2; run++) {
        const processHandle = Bun.spawn([process.execPath, launcher], {
          cwd: dir,
          env: { ...process.env, PATH: `${bin}:${process.env.PATH}` },
          stdout: 'pipe', stderr: 'pipe',
        });
        try {
          const deadline = Date.now() + 5_000;
          while ((await text()).split('advertised').length - 1 <= run) {
            if (Date.now() > deadline) throw new Error('假管理器或网页没有按时启动');
            await Bun.sleep(10);
          }
          processHandle.kill('SIGINT');
          expect(await processHandle.exited).toBe(0);
        } finally {
          processHandle.kill('SIGINT');
        }
      }
      const lines = (await text()).trim().split('\n');
      expect(lines.filter(line => ['backend-ready', 'start vite', 'vite-ready', 'advertised'].includes(line))).toEqual([
        'backend-ready', 'start vite', 'vite-ready', 'advertised',
        'backend-ready', 'start vite', 'vite-ready', 'advertised',
      ]);
      expect(lines.filter(line => line === 'seed')).toHaveLength(1);
      expect(lines.filter(line => line.startsWith('stop'))).toEqual(['stop manager', 'stop vite', 'stop manager', 'stop vite']);
      expect(await readFile(resolve(dir, 'build/_dev/data/example.db'), 'utf8')).toBe('保留');
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  });
});
