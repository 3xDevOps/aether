import { StateIndicator } from '@/components/state-dot'
import { Skeleton } from '@/components/ui/skeleton'
import { timeAgo } from '@/lib/format'
import { useDelayed } from '@/lib/hooks'
import { runLabel, stateLabel, type PresentationState } from '@/lib/status'
import { cn, focusRing } from '@/lib/utils'
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
        <div
          role="alert"
          className="mx-3 my-3 w-[calc(100%-1.5rem)] max-w-[1200px] border-l-2 border-state-failed bg-state-failed/10 px-3 py-2 text-[13px] leading-5 text-state-failed sm:mx-4 sm:w-[calc(100%-2rem)]"
        >
          <span className="break-words whitespace-pre-wrap">
            {dead ? error : 'Cannot reach the server. Retrying.'}
          </span>
        </div>
      )
    }
    if (loading) {
      return (
        <div className="mx-auto grid w-full max-w-[1200px] gap-1.5 px-3 py-2 sm:px-4 sm:py-3">
          <Skeleton className="h-7 rounded-sm" />
          <Skeleton className="h-7 rounded-sm" />
          <Skeleton className="h-7 rounded-sm" />
        </div>
      )
    }
    return hydrated ? (
      <div className="mx-auto flex w-full max-w-[1200px] flex-1 items-start px-3 py-6 sm:px-4 sm:py-8">
        <div className="w-full border-y border-border/80 px-3 py-4">
          <p className="text-[13px] font-medium">{empty}</p>
          <p className="mt-1 text-xs leading-4 text-muted-foreground">
            New runs will appear here as soon as they are launched.
          </p>
        </div>
      </div>
    ) : null
  }

  return (
    <div className="mx-auto w-full max-w-[1200px] px-3 py-2 sm:px-4 sm:py-3">
      <div className="border-y border-border/80">
        <div className="hidden grid-cols-[minmax(0,1fr)_10rem_8rem_7rem] gap-4 border-b px-3 py-1.5 text-[11px] font-medium tracking-wide text-muted-foreground uppercase md:grid">
          <span>Run</span>
          <span>Owner</span>
          <span>Status</span>
          <span className="text-right">Changed</span>
        </div>
        <ul>
          {runs.map(({ run, state, owner }) => (
            <li key={run.id} className="border-b last:border-b-0">
              <button
                type="button"
                onClick={() => navigate('terminal', { runId: run.id })}
                style={{ borderLeftColor: owner?.color }}
                className={cn(
                  focusRing,
                  'grid w-full grid-cols-[minmax(0,1fr)_auto] items-start gap-x-3 gap-y-1 border-l-2 border-l-transparent px-3 py-2 text-left transition-colors hover:bg-toolbar-hover md:grid-cols-[minmax(0,1fr)_10rem_8rem_7rem] md:items-center',
                )}
              >
                <span className="col-start-1 row-start-1 flex min-w-0 items-start gap-2">
                  <StateIndicator state={state} className="mt-1.5" />
                  <span className="min-w-0 flex-1">
                    <span className="block break-words text-[13px] font-medium leading-5">
                      {runLabel(run)}
                    </span>
                    <span className="mt-0.5 block break-words text-xs leading-4 text-muted-foreground select-text">
                      {run.harness} · {run.branch || 'No branch assigned'}
                      {run.reason ? ` · ${run.reason}` : ''}
                    </span>
                    <span className="mt-0.5 block break-words text-xs leading-4 text-muted-foreground select-text md:hidden">
                      {owner?.display_name ?? run.member_id}
                    </span>
                  </span>
                </span>
                <span className="hidden min-w-0 break-words text-xs leading-4 text-muted-foreground md:block">
                  {owner?.display_name ?? run.member_id}
                </span>
                <span className="col-start-1 row-start-2 flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground md:col-auto md:row-auto">
                  <span className="md:hidden">Status</span>
                  <StatusChip state={state} />
                </span>
                <time
                  className="col-start-2 row-start-1 whitespace-nowrap text-right text-xs tabular-nums text-muted-foreground md:col-auto md:row-auto"
                  title={run.stateChangedAt}
                >
                  {timeAgo(run.stateChangedAt)}
                </time>
              </button>
            </li>
          ))}
        </ul>
      </div>
    </div>
  )
}

function StatusChip({ state }: { state: PresentationState }) {
  return (
    <span className="inline-flex min-h-5 items-center gap-1.5 rounded-sm border border-border/80 px-1.5 py-px text-xs leading-4 text-foreground">
      <StateIndicator state={state} decorative />
      <span>{stateLabel[state]}</span>
    </span>
  )
}
