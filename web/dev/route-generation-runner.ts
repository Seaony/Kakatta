import assert from 'node:assert/strict';
import { cp, mkdir, mkdtemp, readFile, readdir, rm, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer } from 'vite';
import { generateRoutes } from '../scripts/generated';

async function run() {
  const web = fileURLToPath(new URL('../', import.meta.url));
  const temp = await mkdtemp(join(tmpdir(), 'kakatta-route-dev-'));
  const output = join(temp, 'src/routeTree.gen.ts');
  let server: Awaited<ReturnType<typeof createServer>> | undefined;
  let main: Awaited<ReturnType<typeof createServer>> | undefined;
  try {
    const dependencies = join(temp, 'shared-dependencies');
    await mkdir(dependencies);
    for (const name of await readdir(join(web, 'node_modules'))) {
      if (!name.startsWith('.vite')) await symlink(join(web, 'node_modules', name), join(dependencies, name));
    }
    const mainRoot = join(temp, 'main');
    await mkdir(mainRoot);
    await symlink(dependencies, join(mainRoot, 'node_modules'), 'dir');
    await writeFile(join(mainRoot, 'package.json'), '{"type":"module"}');
    await writeFile(join(mainRoot, 'index.html'), '<!doctype html><html><body></body></html>');
    main = await createServer({ root: mainRoot, configFile: false, server: { host: '127.0.0.1', port: 0 },
      optimizeDeps: { noDiscovery: true, include: ['react-dom/client'] }, logLevel: 'error' });
    await main.listen();
    const mainCache = join(dependencies, '.vite/deps/_metadata.json');
    const originalCache = await readFile(mainCache, 'utf8');
    const optimizedUrl = new URL('node_modules/.vite/deps/react-dom_client.js?v=' + JSON.parse(originalCache).browserHash,
      main.resolvedUrls!.local[0]);
    assert((await readFile(join(dependencies, '.vite/deps/react-dom_client.js'), 'utf8')).length > 0);
    await mkdir(join(temp, 'scripts'), { recursive: true });
    await cp(join(web, 'src/routes'), join(temp, 'src/routes'), { recursive: true });
    for (const file of ['vite.config.ts', 'tsr.config.json', 'scripts/generated.ts']) {
      await cp(join(web, file), join(temp, file));
    }
    await symlink(dependencies, join(temp, 'node_modules'), 'dir');
    await writeFile(join(temp, 'package.json'), '{"type":"module"}');
    await writeFile(join(temp, 'index.html'), '<!doctype html><html><body></body></html>');
    server = await createServer({ root: temp, configFile: join(temp, 'vite.config.ts'), cacheDir: join(temp, '.vite-cache'),
      server: { host: '127.0.0.1', port: 0, strictPort: false }, optimizeDeps: { noDiscovery: true }, logLevel: 'error' });
    await server.listen();
    const initial = await readFile(output, 'utf8');
    assert.equal(initial, await generateRoutes(temp));
    assert(initial.includes('// 路由表由生成器维护，请修改 routes 目录。'));
    const route = join(temp, 'src/routes/dev-route-notice-check.tsx');
    await writeFile(route, "import { createFileRoute } from '@tanstack/react-router';\nexport const Route = createFileRoute('/dev-route-notice-check')({ component: () => null });\n");
    let changed = '';
    const deadline = Date.now() + 5000;
    while (Date.now() < deadline) {
      changed = await readFile(output, 'utf8');
      if (changed.includes('/dev-route-notice-check')) break;
      await Bun.sleep(50);
    }
    assert(changed.includes('/dev-route-notice-check'));
    assert.equal(changed, await generateRoutes(temp));
    await server.close(); server = undefined;
    assert.equal(await readFile(output, 'utf8'), changed);
    const response = await fetch(optimizedUrl);
    assert.equal(response.status, 200, response.statusText);
    assert.equal(await readFile(mainCache, 'utf8'), originalCache, '临时Vite不能覆盖另一个实例的依赖缓存');
  } finally {
    await server?.close();
    await main?.close();
    await rm(temp, { recursive: true, force: true });
  }
}

if (import.meta.main) await run();
