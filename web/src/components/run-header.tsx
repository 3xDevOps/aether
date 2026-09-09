import { Shield } from 'lucide-react'
import { RunActions } from '@/components/run-actions'
import { StateIndicator } from '@/components/state-dot'
import { ViewHeader } from '@/components/view-header'
import { runLabel, runState, stateLabel } from '@/lib/status'
import { usePendingApprovalRuns } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

/**
 * The title row every run-detail tab opens with. The state travels with the
 * header, so the Terminal, Diff and Events tabs say how the run is doing
 * without sending anyone back to the Overview for it.
 */
export function RunHeader({ run, subtitle }: { run: RunRecord; subtitle?: string }) {
  const pending = usePendingApprovalRuns()
  const state = runState(run.status, pending.has(run.id))

  return (
    <ViewHeader
      title={runLabel(run)}
      titleAdornment={
        <>
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
          <span className="flex shrink-0 items-center gap-2 text-xs text-muted-foreground">
            <StateIndicator state={state} decorative />
            {stateLabel[state]}
          </span>
        </>
      }
      subtitle={subtitle}
      actions={<RunActions run={run} />}
    />
  )
}
