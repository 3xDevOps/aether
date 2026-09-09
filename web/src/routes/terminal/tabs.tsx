import type { Route } from '@/store/ui'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'

/** The run-detail tabs, in strip order. Exported so a caller that has to
 * reason about "any tab of this run" reads the same list the strip draws. */
export const runTabs = [
  { route: 'run', label: 'Overview' },
  { route: 'terminal', label: 'Terminal' },
  { route: 'diff', label: 'Diff' },
  { route: 'events', label: 'Events' },
]

/**
 * True while any run-detail tab for this run is open. Every entry point
 * lands on the terminal, so a selected row cannot key on the Overview route
 * alone and still stay lit while the reader moves between tabs.
 */
export function isRunRoute(route: Route, runID: string): boolean {
  return route.params.runId === runID && runTabs.some((t) => t.route === route.name)
}

/** The run-detail tab strip. Each tab is a route the registry already knows. */
export function RunTabs({ runID, active }: { runID: string; active: string }) {
  const navigate = useStore((s) => s.navigate)

  return (
    <nav aria-label="Run tabs" className="flex gap-1 border-b px-2">
      {runTabs.map(({ route, label }) => (
        <button
          key={route}
          type="button"
          aria-current={route === active ? 'page' : undefined}
          onClick={() => navigate(route, { runId: runID })}
          className={cn(
            'border-b-2 px-2 py-1.5 text-xs',
            route === active
              ? 'border-foreground'
              : 'border-transparent text-muted-foreground hover:text-foreground',
          )}
        >
          {label}
        </button>
      ))}
    </nav>
  )
}
