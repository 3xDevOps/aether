import { fileURLToPath } from 'node:url'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    maxWorkers: 2,
    // Stack runs afterEach in reverse registration order: a file restores
    // timers, testing-library unmounts, then setup drains the focus timer.
    // The default parallel order lets that timer outlive the document.
    sequence: { hooks: 'stack' },
  },
})
