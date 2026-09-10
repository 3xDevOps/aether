import { Shield } from 'lucide-react'
import { RunActions } from '@/components/run-actions'
import { StateIndicator } from '@/components/state-dot'
import { runLabel, runState, stateLabel, type PresentationState } from '@/lib/status'
import { usePendingApprovalRuns } from '@/store/hooks'
import { focusRing } from '@/lib/utils'
import type { RunRecord } from '@/store/runs'

function stateTone(state: PresentationState) {
  switch (state) {
    case 'failed':
      return 'border-state-failed/50 text-state-failed'
    case 'done':
      return 'border-state-done/50 text-state-done'
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

/**
 * The title row every run-detail tab opens with. The state travels with the
 * header, so the Terminal, Diff and Events tabs say how the run is doing
 * without sending anyone back to the Overview for it.
 */
export function RunHeader({ run, subtitle }: { run: RunRecord; subtitle?: string }) {
  const pending = usePendingApprovalRuns()
  const state = runState(run.status, pending.has(run.id))
  const label = runLabel(run)
  const task = run.task.trim()
  const hasTitle = Boolean(run.title?.trim())
  const detail = subtitle?.trim()
  const showTask = Boolean(task && (!hasTitle || task !== label))
  const showDetail = Boolean(detail && detail !== task)
  const reason = run.reason?.trim()

  return (
    <header className="@container/run-header flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 border-b px-3 py-1 sm:px-4">
      <div className="flex min-w-0 flex-grow basis-[22rem] flex-wrap items-center gap-x-2 gap-y-0.5 text-xs leading-4 text-muted-foreground">
        <h1
          className={`min-w-0 break-words text-[15px] font-semibold leading-5 text-foreground ${!hasTitle && task ? 'line-clamp-2' : ''}`}
          title={label}
        >
          {label}
        </h1>
        {run.protected && (
          <span
            role="img"
            aria-label="Protected: only the owner or an admin can steer or kill this run"
            title="Protected: only the owner or an admin can steer or kill this run"
            className="flex shrink-0 items-center text-muted-foreground"
          >
            <Shield className="size-3.5" aria-hidden />
          </span>
        )}
        <span
          className={`inline-flex min-h-5 shrink-0 items-center gap-1 rounded-sm border px-1.5 py-px text-xs leading-4 ${stateTone(state)}`}
        >
          <StateIndicator state={state} decorative />
          <span>{stateLabel[state]}</span>
        </span>
        {showTask && (
          <details className="min-w-0 grow basis-full @md/run-header:basis-[16rem]">
            <summary className={`${focusRing} flex min-w-0 max-w-full cursor-pointer items-start gap-1.5 break-words`}>
              {hasTitle ? (
                <>
                  <span className="min-w-0 flex-1 line-clamp-2 select-text break-words text-foreground/90">
                    {task}
                  </span>
                  <span className="shrink-0 whitespace-nowrap text-[11px] leading-4 text-muted-foreground underline underline-offset-2">
                    View full task
                  </span>
                </>
              ) : (
                <span className="shrink-0 whitespace-nowrap text-[11px] leading-4 text-muted-foreground underline underline-offset-2">
                  View full task
                </span>
              )}
            </summary>
            <div className="mt-1 max-h-32 min-w-0 max-w-full overflow-y-auto rounded-sm border border-border/70 bg-muted/20 px-2 py-1.5">
              <p className="whitespace-pre-wrap break-words select-text text-foreground/90">
                {run.task}
              </p>
            </div>
          </details>
        )}
        {showDetail && (
          <span className="min-w-0 break-words select-text" title={detail}>
            {detail}
          </span>
        )}
        {reason && (
          <span className="min-w-0 break-words select-text" title={reason}>
            Reason: {reason}
          </span>
        )}
        <span className="min-w-0 break-words select-text">
          {run.harness}
          <span className="mx-1 text-muted-foreground/70" aria-hidden>
            ·
          </span>
          {run.mode}
        </span>
      </div>
      <div
        role="toolbar"
        aria-label={`${label} actions`}
        className="flex max-w-full shrink-0 flex-wrap items-center justify-end gap-1"
      >
        <RunActions run={run} />
      </div>
    </header>
  )
}
