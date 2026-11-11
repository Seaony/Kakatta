import { expect, test } from 'bun:test';
import { fileURLToPath } from 'node:url';

// 隔离浏览器连接，避免组件测试的模拟DOM替换原生WebSocket。
test.skipIf(!process.env.KAKATTA_HMR_TEST_URL)('控制台文案热更保持页面状态，恢复原文也即时更新', async () => {
  const child = Bun.spawn([process.execPath, fileURLToPath(new URL('./hmr-browser.ts', import.meta.url))], {
    stdout: 'pipe', stderr: 'pipe', env: process.env,
  });
  const [code, stdout, stderr] = await Promise.all([child.exited, new Response(child.stdout).text(), new Response(child.stderr).text()]);
  expect(code, stdout + stderr).toBe(0);
}, 60_000);
