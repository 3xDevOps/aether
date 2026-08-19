// Route seam. A route file calls registerRoute at module scope and is
// imported once from routes/index.ts; nothing else in the shell changes when
// a view is added.

import type { ComponentType } from 'react'

export interface RouteProps {
  params: Record<string, string>
}

const registry: Record<string, ComponentType<RouteProps>> = {}

export function registerRoute(name: string, view: ComponentType<RouteProps>): void {
  if (registry[name]) throw new Error(`route already registered: ${name}`)
  registry[name] = view
}

export function lookupRoute(name: string): ComponentType<RouteProps> | undefined {
  return registry[name]
}
