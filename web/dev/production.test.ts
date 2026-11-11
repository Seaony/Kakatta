import { expect, test } from 'bun:test';
import { mkdtemp, readdir, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

test('即使设置开发开关，生产构建也不带控制台、专用样式或管理器请求', async () => {
  const output = await mkdtemp(join(tmpdir(), 'kakatta-dev-production-'));
  const web = fileURLToPath(new URL('../', import.meta.url));
  try {
    const child = Bun.spawn([process.execPath, join(web, 'node_modules/vite/bin/vite.js'), 'build', '--outDir', output, '--emptyOutDir'], {
      cwd: web, stdout: 'pipe', stderr: 'pipe',
      env: { ...process.env, KAKATTA_DEVELOPMENT: '1', KAKATTA_DEV_MODE: 'real',
        KAKATTA_DEV_BACKEND: 'http://127.0.0.1:18080', KAKATTA_DEV_CONTROL: 'http://127.0.0.1:18081' },
    });
    const [code, stdout, stderr] = await Promise.all([child.exited, new Response(child.stdout).text(), new Response(child.stderr).text()]);
    expect(code, stdout + stderr).toBe(0);
    const files = await readdir(output, { recursive: true });
    expect(files).toContain('index.html');
    for (const file of files.filter((path) => /\.(html|js|css)$/.test(path))) {
      const source = await readFile(join(output, file), 'utf8');
      for (const marker of ['kakatta-dev-', '/dev/client', '/__dev', 'KAKATTA_DEV_', 'KAKATTA_DEVELOPMENT', 'Development console', '开发控制台']) {
        expect(source, `${file} 不应包含 ${marker}`).not.toContain(marker);
      }
    }
  } finally {
    await rm(output, { recursive: true, force: true });
  }
}, 120_000);
