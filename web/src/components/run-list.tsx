import type { MouseEvent } from 'react'
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
          className="border-y border-state-failed/35 bg-state-failed/10 px-3 py-2 text-[13px] leading-5 text-state-failed sm:px-4"
        >
          <span className="break-words whitespace-pre-wrap">
            {dead ? error : 'Cannot reach the server. Retrying.'}
          </span>
        </div>
      )
    }
    if (loading) {
      return (
        <div className="grid gap-0 border-y border-border px-3 py-2 sm:px-4">
          <Skeleton className="h-7 rounded-none" />
          <Skeleton className="h-7 rounded-none" />
          <Skeleton className="h-7 rounded-none" />
        </div>
      )
    }
    return hydrated ? (
      <div className="w-full px-3 py-4 sm:px-4 sm:py-5">
        <div className="border-y border-border px-3 py-3">
          <p className="text-[13px] font-medium">{empty}</p>
          <p className="mt-1 text-xs leading-4 text-muted-foreground">
            New runs will appear here as soon as they are launched.
          </p>
        </div>
      </div>
    ) : null
  }

  return (
    <div className="w-full px-3 py-2 sm:px-4 sm:py-3">
      <div className="border-y border-border">
        <div className="hidden grid-cols-[minmax(0,1fr)_10rem_8rem_7rem] gap-4 border-b bg-sidebar/30 px-3 py-1.5 text-[11px] font-medium tracking-wide text-muted-foreground uppercase md:grid">
          <span>Run</span>
          <span>Owner</span>
          <span>Status</span>
          <span className="text-right">Changed</span>
        </div>
        <ul>
          {runs.map(({ run, state, owner }) => (
            <li key={run.id} className="border-b last:border-b-0">
              <div
                onClick={(event: MouseEvent<HTMLDivElement>) => {
                  const target = event.target
                  if (
                    target instanceof Element &&
                    target.closest('button, a, input, select, textarea, [data-run-navigation-exempt]')
                  ) {
                    return
                  }
                  navigate('terminal', { runId: run.id })
                }}
                style={{ borderLeftColor: owner?.color }}
                className={cn(
                  focusRing,
                  'grid w-full cursor-pointer grid-cols-[minmax(0,1fr)_auto] items-start gap-x-3 gap-y-1 border-l-2 border-l-transparent px-3 py-2 text-left transition-colors hover:bg-toolbar-hover md:grid-cols-[minmax(0,1fr)_10rem_8rem_7rem] md:items-center',
                )}
              >
                <div className="col-start-1 row-start-1 flex min-w-0 items-start gap-2 md:col-auto md:row-auto">
                  <StateIndicator state={state} className="mt-1.5" />
                  <div className="min-w-0 flex-1">
                    <button
                      type="button"
                      aria-label={runLabel(run)}
                      onClick={(event) => {
                        event.stopPropagation()
                        navigate('terminal', { runId: run.id })
                      }}
                      className={cn(
                        focusRing,
                        'block min-h-[22px] max-w-full rounded-[2px] text-left',
                      )}
                    >
                      <span className="block line-clamp-2 break-words text-[13px] font-medium leading-5">
                        {runLabel(run)}
                      </span>
                    </button>
                    <span className="mt-0.5 block break-words text-xs leading-4 text-muted-foreground">
                      <span>{run.harness}</span>
                      <span aria-hidden> · </span>
                      <span
                        data-run-navigation-exempt={run.branch ? 'true' : undefined}
                        className={run.branch ? 'select-text font-mono' : undefined}
                        title={run.branch || undefined}
                        onMouseDown={
                          run.branch
                            ? (event) => event.stopPropagation()
                            : undefined
                        }
                        onClick={
                          run.branch
                            ? (event) => event.stopPropagation()
                            : undefined
                        }
                      >
                        {run.branch || 'No branch assigned'}
                      </span>
                      {run.reason && (
                        <>
                          <span aria-hidden> · </span>
                          <span>{run.reason}</span>
                        </>
                      )}
                    </span>
                    <span className="mt-0.5 block break-words text-xs leading-4 text-muted-foreground md:hidden">
                      {owner?.display_name ?? run.member_id}
                    </span>
                  </div>
                </div>
                <span className="hidden min-w-0 items-center gap-2 break-words text-xs leading-4 text-muted-foreground md:flex">
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
              </div>
            </li>
          ))}
        </ul>
      </div>
    </div>
  )
}

function StatusChip({ state }: { state: PresentationState }) {
  return (
    <span className="inline-flex min-h-5 items-center gap-1.5 rounded-[2px] border border-border/80 px-1.5 py-px text-xs leading-4 text-foreground">
      <StateIndicator state={state} decorative />
      <span>{stateLabel[state]}</span>
    </span>
  )
}
