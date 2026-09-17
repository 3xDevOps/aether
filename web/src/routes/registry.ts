// Route seam. A route file calls registerRoute at module scope and is
// imported once from routes/index.ts; nothing else in the shell changes when
// a view is added.

import type { ComponentType } from 'react'

export interface RouteProps {
  params: Record<string, string>
  /** Terminal routes are inactive while retained by CenterView's cache. */
  active?: boolean
  /** Reports the number of parsed terminal cells held by an inactive view. */
  onTerminalWeight?: (cells: number) => void
  /** Reports that the cached terminal can no longer be resumed. */
  onTerminalInvalidate?: () => void
}

const registry: Record<string, ComponentType<RouteProps>> = {}

export function registerRoute(name: string, view: ComponentType<RouteProps>): void {
  if (registry[name]) throw new Error(`route already registered: ${name}`)
  registry[name] = view
}

export function lookupRoute(name: string): ComponentType<RouteProps> | undefined {
  return registry[name]
}

/** Every registered view, so a sweep over "all surfaces" cannot go stale as
 * routes are added. */
export function registeredRoutes(): string[] {
  return Object.keys(registry)
}
