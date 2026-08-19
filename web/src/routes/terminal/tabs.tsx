import { cn } from '@/lib/utils'
import { useStore } from '@/store'

const tabs = [
  { route: 'run', label: 'Overview' },
  { route: 'terminal', label: 'Terminal' },
  { route: 'diff', label: 'Diff' },
  { route: 'events', label: 'Events' },
]

/** The run-detail tab strip. Each tab is a route the registry already knows. */
export function RunTabs({ runID, active }: { runID: string; active: string }) {
  const navigate = useStore((s) => s.navigate)

  return (
    <nav className="flex gap-1 border-b px-2">
      {tabs.map(({ route, label }) => (
        <button
          key={route}
          type="button"
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
