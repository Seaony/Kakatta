import react from '@vitejs/plugin-react';
import { tanstackRouter } from '@tanstack/router-plugin/vite';
import { defineConfig } from 'vite';
import { readFile, writeFile } from 'node:fs/promises';
import { normalizeRouteNotice, routeOutput } from './scripts/generated.ts';

export default defineConfig({
  plugins: [
    tanstackRouter({ target: 'react', autoCodeSplitting: true }),
    react(),
    {
      name: 'local-route-notice',
      async buildEnd() {
        const source = await readFile(routeOutput, 'utf8');
        const normalized = normalizeRouteNotice(source);
        if (source !== normalized) await writeFile(routeOutput, normalized);
      },
    },
  ],
  build: {
    target: 'safari16',
    cssTarget: 'safari16',
    outDir: 'dist',
    emptyOutDir: true,
  },
});
