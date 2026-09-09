import { StateIndicator } from '@/components/state-dot'
import { Skeleton } from '@/components/ui/skeleton'
import { timeAgo } from '@/lib/format'
import { useDelayed } from '@/lib/hooks'
import { runLabel } from '@/lib/status'
import { useStore } from '@/store'
import type { SidebarRun } from '@/store/selectors'

export function RunList({ runs, empty }: { runs: SidebarRun[]; empty: string }) {
  const hydrated = useStore((s) => s.hydrated)
  const error = useStore((s) => s.hydrationError)
  const dead = useStore((s) => s.streamDead)
  const navigate = useStore((s) => s.navigate)
  const unreachable = error !== null
  const loading = useDelayed(!hydrated && !unreachable && runs.length === 0)

  if (runs.length === 0) {
    if (unreachable) {
      // A dead token is not an unreachable server: nothing retries, and only
      // a fresh token helps, so the pane says what the error recorded.
      return (
        <p className="p-4 text-sm text-muted-foreground">
          {dead ? error : 'Cannot reach the server. Retrying.'}
        </p>
      )
    }
    return loading ? (
      <div className="space-y-2 p-4">
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-10 w-2/3" />
      </div>
    ) : (
      <p className="p-4 text-sm text-muted-foreground">{hydrated ? empty : ''}</p>
    )
  }

  return (
    <ul className="divide-y">
      {runs.map(({ run, state, owner }) => (
        <li key={run.id}>
          <button
            type="button"
            onClick={() => navigate('terminal', { runId: run.id })}
            style={{ borderLeftColor: owner?.color }}
            className="flex w-full items-center gap-3 border-l-2 border-l-transparent px-4 py-2 text-left hover:bg-accent/60"
          >
            <StateIndicator state={state} />
            <span className="min-w-0 flex-1">
              <span className="block truncate text-sm">{runLabel(run)}</span>
              <span className="block truncate text-xs text-muted-foreground">
                {run.harness} · {run.branch}
                {run.reason ? ` · ${run.reason}` : ''}
              </span>
            </span>
            <span className="shrink-0 text-xs text-muted-foreground">
              {owner?.display_name ?? run.member_id}
            </span>
            <span className="shrink-0 text-xs text-muted-foreground">
              {timeAgo(run.stateChangedAt)}
            </span>
          </button>
        </li>
      ))}
    </ul>
  )
}
