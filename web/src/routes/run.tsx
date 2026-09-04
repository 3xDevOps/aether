import { RunActions } from '@/components/run-actions'
import { StateDot } from '@/components/state-dot'
import { ViewHeader } from '@/components/view-header'
import { timeAgo } from '@/lib/format'
import { pendingApprovalRuns, runLabel, runState, stateLabel } from '@/lib/status'
import { EnvironmentBanner } from '@/routes/onboarding/environment-step'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { RunTabs } from '@/routes/terminal/tabs'
import { useStore } from '@/store'

export function RunView({ params }: RouteProps) {
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
      <ViewHeader
        title={runLabel(run)}
        subtitle={run.branch}
        actions={<RunActions run={run} />}
      />
      <RunTabs runID={run.id} active="run" />
      {/* Nothing renders here until this session started an environment
          build for the run's workspace; hidden when the banner is null so
          the padding leaves no phantom gap. */}
      <div className="px-4 pt-4 empty:hidden">
        <EnvironmentBanner workspaceId={run.workspace_id} />
      </div>
      <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 p-4 text-sm">
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
        {run.last_commit_at && (
          <>
            <dt className="text-muted-foreground">Last commit</dt>
            <dd title={run.last_commit}>
              <code>{run.last_commit?.slice(0, 8)}</code>{' '}
              {timeAgo(run.last_commit_at)}
            </dd>
          </>
        )}
      </dl>
    </div>
  )
}

registerRoute('run', RunView)
