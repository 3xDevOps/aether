import { Shield } from 'lucide-react'
import { RunActions } from '@/components/run-actions'
import { StateIndicator } from '@/components/state-dot'
import { runLabel, runState, stateLabel, type PresentationState } from '@/lib/status'
import { focusRing } from '@/lib/utils'
import { RunTabs } from '@/routes/terminal/tabs'
import { usePendingApprovalRuns } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

function stateTone(state: PresentationState) {
  switch (state) {
    case 'failed':
      return 'border-state-failed/50 text-state-failed'
    case 'done':
      return 'border-state-done/50 text-success-foreground'
    case 'waiting':
      return 'border-state-waiting/50 text-state-waiting'
    case 'needs-attention':
      return 'border-state-needs-attention/50 text-state-needs-attention'
    case 'working':
      return 'border-state-working/50 text-state-working'
    default:
      return 'border-border/80 text-muted-foreground'
  }
}

function reasonTone(state: PresentationState) {
  switch (state) {
    case 'failed':
      return 'border-state-failed bg-state-failed/10'
    case 'needs-attention':
      return 'border-state-needs-attention/60 bg-state-needs-attention/10'
    default:
      return 'border-border/80 bg-muted/20'
  }
}

/**
 * The title row every run-detail tab opens with. The state travels with the
 * header, so the Terminal, Diff and Events tabs say how the run is doing
 * without sending anyone back to the Overview for it.
 */
export function RunHeader({
  run,
  subtitle,
  active,
}: {
  run: RunRecord
  subtitle?: string
  active: string
}) {
  const pending = usePendingApprovalRuns()
  const state = runState(run.status, pending.has(run.id))
  const label = runLabel(run)
  const task = run.task.trim()
  const hasTitle = Boolean(run.title?.trim())
  const detail = subtitle?.trim()
  const showDetail = Boolean(detail && detail !== task)

  return (
    <div className="@container/run-header min-w-0 shrink-0">
      <header className="min-w-0 border-b border-border/80">
        <div className="min-w-0 px-3 py-1 sm:px-4">
          <div className="flex min-w-0 items-start gap-2">
            <h1
              className="min-w-0 flex-1 break-words text-[15px] font-semibold leading-5 text-foreground"
              title={label}
            >
              {label}
            </h1>
            {run.protected && (
              <span
                role="img"
                aria-label="Protected: only the owner or an admin can steer or kill this run"
                title="Protected: only the owner or an admin can steer or kill this run"
                className="flex shrink-0 items-center pt-0.5 text-muted-foreground"
              >
                <Shield className="size-3.5" aria-hidden />
              </span>
            )}
          </div>
          {task && (
            <details className="mt-0.5 min-w-0 max-w-full">
              <summary
                className={`${focusRing} flex min-w-0 max-w-full cursor-pointer items-start gap-1.5 break-words text-xs leading-4 text-muted-foreground`}
              >
                {hasTitle ? (
                  <span className="min-w-0 flex-1 line-clamp-1 select-text break-words text-foreground/90">
                    {task}
                  </span>
                ) : (
                  <span className="min-w-0 flex-1 truncate text-foreground/90">Task details</span>
                )}
                <span className="shrink-0 whitespace-nowrap text-[11px] leading-4 text-muted-foreground underline underline-offset-2">
                  View full task
                </span>
              </summary>
              <div
                tabIndex={0}
                className={`${focusRing} mt-1 max-h-40 min-w-0 max-w-full overflow-y-auto border border-border/70 bg-muted/20 px-2 py-1.5`}
              >
                <p className="whitespace-pre-wrap break-words select-text text-[13px] leading-5 text-foreground/90">
                  {run.task}
                </p>
              </div>
            </details>
          )}
          {run.reason && (
            <div
              tabIndex={0}
              className={`${focusRing} mt-1 max-h-24 min-w-0 max-w-full overflow-y-auto border-l-2 px-2 py-1 text-[13px] leading-5 text-foreground/90 ${reasonTone(state)}`}
            >
              <span className="mr-1.5 font-medium text-muted-foreground">Reason</span>
              <span className="whitespace-pre-wrap break-words select-text">{run.reason}</span>
            </div>
          )}
        </div>
        <div className="flex min-h-9 min-w-0 items-stretch overflow-hidden border-t border-border bg-sidebar">
          <div className="flex min-w-0 flex-[1_1_14rem] items-center gap-2 overflow-x-auto px-3 text-xs leading-4 text-muted-foreground [scrollbar-width:none] sm:px-4 [&::-webkit-scrollbar]:hidden">
            <span
              className={`inline-flex min-h-5 shrink-0 items-center gap-1.5 rounded-sm border px-2 py-px text-xs leading-4 ${stateTone(state)}`}
            >
              <StateIndicator
                state={state}
                decorative
                className={state === 'working' ? 'w-4' : undefined}
              />
              <span>{stateLabel[state]}</span>
            </span>
            <span className="shrink-0 whitespace-nowrap font-mono text-[11px]">
              {run.harness}
              <span className="mx-1 text-muted-foreground/70" aria-hidden>
                /
              </span>
              {run.mode}
            </span>
            {showDetail && (
              <span
                className="shrink-0 whitespace-nowrap font-mono text-[11px] select-text"
                title={detail}
              >
                {detail}
              </span>
            )}
          </div>
          <RunTabs runID={run.id} active={active} />
          <div
            role="toolbar"
            aria-label={`${label} actions`}
            className="flex shrink-0 items-center gap-1 px-2 sm:pr-4"
          >
            <RunActions run={run} />
          </div>
        </div>
      </header>
    </div>
  )
}
