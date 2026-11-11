import react from '@vitejs/plugin-react';
import { tanstackRouter } from '@tanstack/router-plugin/vite';
import { defineConfig, type Plugin, type ProxyOptions } from 'vite';
import { routeNoticePlugin } from './scripts/generated.ts';

export function developmentTools(command: string, env: Record<string, string | undefined>): {
  plugins: Plugin[];
  proxy: Record<string, ProxyOptions>;
} {
  if (command !== 'serve' || env.KAKATTA_DEVELOPMENT !== '1') return { plugins: [], proxy: {} };
  const mode = env.KAKATTA_DEV_MODE;
  if (mode !== 'fake' && mode !== 'real') throw new Error('开发模式必须设为 fake 或 real');
  const backend = localEndpoint(env.KAKATTA_DEV_BACKEND);
  const control = localEndpoint(env.KAKATTA_DEV_CONTROL);
  return {
    proxy: {
      '/api': { target: backend, ws: true, changeOrigin: false },
      '/remote': { target: backend, ws: true, changeOrigin: false },
      '/mcp': { target: backend, ws: true, changeOrigin: false },
      '/healthz': { target: backend, changeOrigin: false },
      '/__dev': { target: control, changeOrigin: false },
    },
    plugins: [{
      name: 'local-development-console',
      apply: 'serve',
      transformIndexHtml() {
        return [{ tag: 'script', attrs: { type: 'module', src: `/dev/client.tsx?mode=${mode}` }, injectTo: 'body' }];
      },
    }],
  };
}

function localEndpoint(value: string | undefined): string {
  const url = new URL(value ?? '');
  if (url.protocol !== 'http:' || !['127.0.0.1', 'localhost', '[::1]'].includes(url.hostname)
      || url.username || url.password || url.pathname !== '/' || url.search || url.hash) {
    throw new Error('开发代理只接受本机 HTTP 地址');
  }
  return url.origin;
}

export default defineConfig(({ command, isPreview }) => {
  const development = developmentTools(isPreview ? 'preview' : command, process.env);
  return {
    server: { proxy: development.proxy },
    plugins: [
      ...development.plugins,
      tanstackRouter({ target: 'react', autoCodeSplitting: true, plugins: [routeNoticePlugin] }),
      react(),
    ],
    build: {
      target: 'safari16',
      cssTarget: 'safari16',
      outDir: 'dist',
      emptyOutDir: true,
    },
  };
});
