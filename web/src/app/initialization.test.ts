import { expect, test } from 'bun:test';
import { resolve } from 'node:path';

test('完整路由首次初始化在普通与严格模式下进入账号引导', async () => {
  const child = Bun.spawn([process.execPath, 'test', './src/test/fresh-setup.fixture.tsx'], {
    cwd: resolve(import.meta.dir, '../..'), stdout: 'pipe', stderr: 'pipe',
  });
  let timedOut = false;
  const timer = setTimeout(() => { timedOut = true; child.kill('SIGKILL'); }, 6_000);
  try {
    const [code, output, errors] = await Promise.all([child.exited, new Response(child.stdout).text(), new Response(child.stderr).text()]);
    if (timedOut || code !== 0) throw new Error(`初始化子进程没有正常完成（超时=${timedOut}，退出码=${code}）：\n${output}\n${errors}`);
    const reports = output.split('\n').filter((line) => line.startsWith('FRESH_SETUP_RESULT '))
      .map((line) => JSON.parse(line.slice('FRESH_SETUP_RESULT '.length)) as { strict: boolean; ready: boolean; events: number });
    expect(reports.map((report) => report.strict)).toEqual([false, true]);
    for (const report of reports) {
      expect(report.ready).toBe(true);
      expect(report.events).toBeGreaterThan(0);
      expect(report.events).toBeLessThanOrEqual(20);
    }
  } finally {
    clearTimeout(timer);
    child.kill('SIGKILL');
    await child.exited;
  }
}, 9_000);
