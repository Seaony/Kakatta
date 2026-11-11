import { expect, test } from 'bun:test';
import { developmentTools } from '../vite.config';

const env = { KAKATTA_DEVELOPMENT: '1', KAKATTA_DEV_MODE: 'fake',
  KAKATTA_DEV_BACKEND: 'http://127.0.0.1:18080', KAKATTA_DEV_CONTROL: 'http://127.0.0.1:18081' };

test.each([
  ['build', env], ['preview', env], ['serve', {}], ['serve', { ...env, KAKATTA_DEVELOPMENT: 'true' }],
])('%s 必须同时满足serve和显式开关才接入控制台及代理', (command, config) => {
  expect(developmentTools(command, config)).toEqual({ plugins: [], proxy: {} });
});

test.each(['fake', 'real'])('%s 仅在开发HTML注入独立模块，HTTP和WebSocket保留Host及Origin', async (mode) => {
  const config = developmentTools('serve', { ...env, KAKATTA_DEV_MODE: mode });
  expect(Object.keys(config.proxy)).toEqual(['/api', '/remote', '/mcp', '/healthz', '/__dev']);
  for (const path of ['/api', '/remote', '/mcp', '/healthz']) {
    expect(config.proxy[path]?.target).toBe(env.KAKATTA_DEV_BACKEND);
    expect(config.proxy[path]?.changeOrigin).toBe(false);
    expect(config.proxy[path]?.headers).toBeUndefined();
  }
  for (const path of ['/api', '/remote', '/mcp']) expect(config.proxy[path]?.ws).toBe(true);
  expect(config.proxy['/__dev']?.target).toBe(env.KAKATTA_DEV_CONTROL);
  expect(config.plugins).toHaveLength(1);
  expect(config.plugins[0]?.apply).toBe('serve');
  const hook = config.plugins[0]?.transformIndexHtml;
  if (typeof hook !== 'function') throw new Error('缺少HTML注入');
  const tags = await hook.call({} as never, '', {} as never);
  expect(tags).toEqual([{ tag: 'script', attrs: { type: 'module', src: `/dev/client.tsx?mode=${mode}` }, injectTo: 'body' }]);
});

test('开发代理拒绝非本机目标和无效模式，生产构建不读取开发配置', () => {
  expect(() => developmentTools('serve', { ...env, KAKATTA_DEV_MODE: 'other' })).toThrow();
  for (const target of ['https://127.0.0.1:18080', 'http://example.invalid', 'http://127.0.0.1:18080/path', 'http://secret@localhost']) {
    expect(() => developmentTools('serve', { ...env, KAKATTA_DEV_BACKEND: target })).toThrow();
  }
  expect(developmentTools('build', { ...env, KAKATTA_DEV_BACKEND: 'invalid' })).toEqual({ plugins: [], proxy: {} });
});
