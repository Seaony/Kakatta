import { expect, test } from 'bun:test';
import { fileURLToPath } from 'node:url';

test.skipIf(!process.env.KAKATTA_LIVE_EVENTS_CHROME)('默认浏览器连续硬导航不耗尽事件连接，缓存后退前进恢复查询和实时更新', async () => {
  const child = Bun.spawn([process.execPath, fileURLToPath(new URL('../test/live-events-browser.ts', import.meta.url))], {
    stdout: 'pipe', stderr: 'pipe', env: process.env,
  });
  const [code, stdout, stderr] = await Promise.all([child.exited, new Response(child.stdout).text(), new Response(child.stderr).text()]);
  expect(code, stdout + stderr).toBe(0);
}, 60_000);
