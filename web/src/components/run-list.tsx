import { StateIndicator } from '@/components/state-dot'
import { Chip } from '@/components/ui/heroui'
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
          className="mx-auto my-4 w-[calc(100%-2rem)] max-w-[1200px] rounded-lg border border-state-failed/30 bg-state-failed/10 p-4 text-sm text-state-failed sm:my-6"
        >
          {dead ? error : 'Cannot reach the server. Retrying.'}
        </div>
      )
    }
    if (loading) {
      return (
        <div className="mx-auto grid w-full max-w-[1200px] gap-2 p-4 sm:p-6">
          <Skeleton className="h-16 rounded-md" />
          <Skeleton className="h-16 rounded-md" />
          <Skeleton className="h-16 rounded-md" />
        </div>
      )
    }
    return hydrated ? (
      <div className="mx-auto flex w-full max-w-[1200px] flex-1 items-center justify-center p-6">
        <div className="w-full max-w-lg rounded-lg border border-dashed p-8 text-center">
          <p className="text-base font-medium">{empty}</p>
          <p className="mt-2 text-[13px] leading-5 text-muted-foreground">
            New runs will appear here as soon as they are launched.
          </p>
        </div>
      </div>
    ) : null
  }

  return (
    <div className="mx-auto w-full max-w-[1200px] p-4 sm:p-6">
      <div className="overflow-hidden rounded-lg border bg-card shadow-xs">
        <div className="hidden grid-cols-[minmax(0,1fr)_10rem_8rem_7rem] gap-4 border-b bg-muted/30 px-4 py-2 text-xs font-medium tracking-wide text-muted-foreground uppercase md:grid">
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
                  'relative grid w-full grid-cols-1 items-center gap-3 border-l-2 border-l-transparent px-4 py-3 pr-24 text-left transition-colors hover:bg-accent/50 sm:px-5 md:grid-cols-[minmax(0,1fr)_10rem_8rem_7rem] md:gap-4 md:pr-5',
                )}
              >
                <span className="flex min-w-0 items-start gap-3">
                  <StateIndicator state={state} className="mt-1.5" />
                  <span className="min-w-0">
                    <span className="block truncate text-sm font-medium">
                      {runLabel(run)}
                    </span>
                    <span className="mt-1 block truncate text-[13px] text-muted-foreground">
                      {run.harness} · {run.branch || 'No branch assigned'}
                      {run.reason ? ` · ${run.reason}` : ''}
                    </span>
                    <span className="mt-1 block truncate text-xs text-muted-foreground md:hidden">
                      {owner?.display_name ?? run.member_id}
                    </span>
                  </span>
                </span>
                <span className="hidden truncate text-[13px] text-muted-foreground md:block">
                  {owner?.display_name ?? run.member_id}
                </span>
                <span className="flex items-center gap-2 text-[13px] text-muted-foreground md:block">
                  <span className="md:hidden">Status</span>
                  <StatusChip state={state} />
                </span>
                <time
                  className="absolute right-4 top-3 text-xs text-muted-foreground sm:right-5 md:static md:text-right"
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

const stateChipColor: Record<
  PresentationState,
  'accent' | 'danger' | 'default' | 'success' | 'warning'
> = {
  'needs-attention': 'warning',
  failed: 'danger',
  working: 'accent',
  waiting: 'default',
  done: 'success',
  idle: 'default',
}

function StatusChip({ state }: { state: PresentationState }) {
  return (
    <Chip color={stateChipColor[state]} variant="soft" size="sm">
      <Chip.Label>{stateLabel[state]}</Chip.Label>
    </Chip>
  )
}
