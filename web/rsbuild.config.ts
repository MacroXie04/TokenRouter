import { defineConfig } from '@rsbuild/core';
import { pluginReact } from '@rsbuild/plugin-react';

export default defineConfig({
  plugins: [pluginReact()],
  source: {
    entry: { index: './src/app/bootstrap.tsx' },
  },
  html: {
    title: 'TokenRouter',
    favicon: false,
  },
  output: {
    distPath: { root: 'dist' },
  },
  server: {
    port: 5173,
  },
});
