import { StateDot } from '@/components/state-dot'
import { timeAgo } from '@/lib/format'
import { pendingApprovalRuns, runState, stateLabel } from '@/lib/status'
import { ViewHeader } from '@/components/view-header'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { RunTabs } from '@/routes/terminal/tabs'
import { useStore } from '@/store'

function RunView({ params }: RouteProps) {
  const run = useStore((s) => s.runs[params.runId])
  const owner = useStore((s) => (run ? s.members[run.member_id] : undefined))
  const pendingApproval = useStore((s) =>
    pendingApprovalRuns(s.inbox).has(params.runId),
  )

  if (!run) {
    return <p className="p-4 text-sm text-muted-foreground">Unknown run.</p>
  }
  const state = runState(run.status, pendingApproval)

  return (
    <div className="flex h-full flex-col">
      <ViewHeader title={run.task} subtitle={run.branch} />
      <RunTabs runID={run.id} active="run" />
      <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-1 p-4 text-sm">
        <dt className="text-muted-foreground">State</dt>
        <dd className="flex items-center gap-2">
          <StateDot state={state} />
          {stateLabel[state]}
          {run.reason && (
            <span className="text-muted-foreground">- {run.reason}</span>
          )}
        </dd>
        <dt className="text-muted-foreground">Harness</dt>
        <dd>
          {run.harness} ({run.mode})
        </dd>
        <dt className="text-muted-foreground">Owner</dt>
        <dd style={{ color: owner?.color }}>
          {owner?.display_name ?? run.member_id}
        </dd>
        <dt className="text-muted-foreground">Created</dt>
        <dd>{timeAgo(run.created_at)}</dd>
        <dt className="text-muted-foreground">Changed</dt>
        <dd>{timeAgo(run.stateChangedAt)}</dd>
      </dl>
    </div>
  )
}

registerRoute('run', RunView)
