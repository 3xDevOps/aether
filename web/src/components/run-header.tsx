import { Shield } from 'lucide-react'
import { Chip } from '@/components/ui/heroui'
import { RunActions } from '@/components/run-actions'
import { StateIndicator } from '@/components/state-dot'
import { runLabel, runState, stateLabel, type PresentationState } from '@/lib/status'
import { usePendingApprovalRuns } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

function stateColor(state: PresentationState) {
  switch (state) {
    case 'failed':
      return 'danger' as const
    case 'done':
      return 'success' as const
    case 'needs-attention':
    case 'waiting':
      return 'warning' as const
    case 'working':
      return 'accent' as const
    default:
      return 'default' as const
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
  const showTask = Boolean(task && task !== label)

  return (
    <header className="@container/run-header flex min-w-0 flex-wrap items-center gap-x-4 gap-y-3 border-b bg-card/45 px-4 py-3">
      <div className="min-w-0 flex-1 basis-64">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1">
          <h1
            className="min-w-0 max-w-full text-xl font-semibold leading-6 tracking-tight"
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
              <Shield className="size-4" aria-hidden />
            </span>
          )}
          <Chip color={stateColor(state)} variant="soft" size="sm" className="gap-1.5">
            <StateIndicator state={state} decorative />
            <Chip.Label>{stateLabel[state]}</Chip.Label>
          </Chip>
        </div>
        <div className="mt-1.5 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-[13px] leading-5 text-muted-foreground">
          {showTask && <span className="min-w-0 max-w-full break-words">{task}</span>}
          {subtitle && (
            <span className="min-w-0 max-w-full break-words" title={subtitle}>
              {subtitle}
            </span>
          )}
          <span className="rounded-sm border border-border/70 bg-muted/45 px-1.5 py-0.5 font-medium text-foreground/75">
            {run.harness}
            <span className="mx-1 text-muted-foreground/70" aria-hidden>
              ·
            </span>
            {run.mode}
          </span>
        </div>
      </div>
      <div
        role="toolbar"
        aria-label={`${label} actions`}
        className="flex min-w-0 max-w-full shrink-0 flex-wrap items-center justify-end gap-1.5"
      >
        <RunActions run={run} />
      </div>
    </header>
  )
}
