/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'node:path'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { '@': path.resolve(__dirname, './src') } },
  build: {
    outDir: path.resolve(__dirname, '../internal/web/dist'),
    emptyOutDir: false, // keep committed dist/.gitkeep (embed placeholder) from being wiped each build

    modulePreload: { polyfill: false }, // no inline polyfill script -> CSP script-src 'self'
  },
  server: { proxy: { '/api': 'http://localhost:8080', '/auth': 'http://localhost:8080', '/s/': 'http://localhost:8080', '/skill.md': 'http://localhost:8080', '/install.md': 'http://localhost:8080' } },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: './src/test-setup.ts',
  },
})
