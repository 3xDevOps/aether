/// <reference types="vitest/config" />
import { fileURLToPath } from 'node:url'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// `bun run dev` serves the SPA on its own port and proxies the API and the
// WebSockets to a running aether-server dashboard gateway.
const gateway = process.env.AETHER_DASHBOARD ?? 'http://127.0.0.1:8080'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  // public/.gitkeep is copied into dist on every build, so `go:embed all:dist`
  // always has a file to match even when the build output is not committed.
  build: { outDir: 'dist' },
  server: {
    proxy: {
      '/api': { target: gateway, changeOrigin: true },
      '/ws': { target: gateway.replace(/^http/, 'ws'), ws: true },
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
  },
})
