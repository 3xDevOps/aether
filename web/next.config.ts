import { PHASE_DEVELOPMENT_SERVER } from 'next/constants'
import type { NextConfig } from 'next'


// `?raw` imports a file's text, as Vite does for Vitest.
const turbopack: NextConfig['turbopack'] = {
  rules: { '*.svg': { condition: { query: '?raw' }, loaders: ['./raw-loader.cjs'], as: '*.js' } },
}

export default function nextConfig(phase: string): NextConfig {
  if (phase === PHASE_DEVELOPMENT_SERVER) {
    // `dev-server.mjs` owns the configured gateway proxy because Next's
    // external rewrites replace Host during WebSocket upgrades.
    return { experimental: { cpus: 2 }, turbopack }
  }

  // Static export is the production artifact consumed by web/embed.go. The
  // development-only proxy lives in dev-server.mjs because this mode cannot
  // carry an external rewrite proxy.
  return {
    experimental: { cpus: 2 },
    turbopack,
    output: 'export',
    distDir: 'dist',
  }
}
