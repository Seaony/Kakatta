import { expect, test } from 'bun:test';
import { fileURLToPath } from 'node:url';

test('开发启动、路由变更和关闭后的路由表均与独立生成器一致', async () => {
  const child = Bun.spawn([process.execPath, fileURLToPath(new URL('./route-generation-runner.ts', import.meta.url))], {
    stdout: 'pipe', stderr: 'pipe',
  });
  const [code, stdout, stderr] = await Promise.all([child.exited, new Response(child.stdout).text(), new Response(child.stderr).text()]);
  expect(code, stdout + stderr).toBe(0);
}, 30_000);
