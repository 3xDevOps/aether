import type { Route } from '@/store/ui'

export const runViews = ['session', 'terminal', 'changes', 'browser'] as const

export type RunView = (typeof runViews)[number]

export const runViewLabel: Record<RunView, string> = {
  session: 'Session',
  terminal: 'Terminal',
  changes: 'Changes',
  browser: 'Browser',
}

export function isRunView(value: string | undefined): value is RunView {
  return (runViews as readonly string[]).includes(value ?? '')
}

export function isRunRoute(route: Route, runID: string): boolean {
  return route.name === 'run' && route.params.runId === runID
}

export function runRoute(runID: string, view?: RunView): Route {
  return { name: 'run', params: view ? { runId: runID, view } : { runId: runID } }
}

export function defaultView(run: { mode: string; acp?: boolean }): RunView {
  return run.mode === 'acp' || run.acp ? 'session' : 'terminal'
}
