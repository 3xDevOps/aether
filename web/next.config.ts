import { PHASE_DEVELOPMENT_SERVER } from 'next/constants'
import type { NextConfig } from 'next'


export default function nextConfig(phase: string): NextConfig {
  if (phase === PHASE_DEVELOPMENT_SERVER) {
    // `dev-server.mjs` owns the configured gateway proxy because Next's
    // external rewrites replace Host during WebSocket upgrades.
    return { experimental: { cpus: 2 } }
  }

  // Static export is the production artifact consumed by web/embed.go. The
  // development-only proxy lives in dev-server.mjs because this mode cannot
  // carry an external rewrite proxy.
  return {
    experimental: { cpus: 2 },
    output: 'export',
    distDir: 'dist',
  }
}
