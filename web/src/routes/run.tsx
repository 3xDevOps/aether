import { MissingRun } from '@/components/missing-run'
import { RunHeader } from '@/components/run-header'
import { timeAgo } from '@/lib/format'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { RunTabs, runTabPanel } from '@/routes/terminal/tabs'
import { useStore } from '@/store'

export function RunView({ params }: RouteProps) {
  const run = useStore((s) => s.runs[params.runId])
  const owner = useStore((s) => (run ? s.members[run.member_id] : undefined))
  const account = useStore((s) =>
    run?.account_member_id ? s.members[run.account_member_id] : undefined,
  )

  if (!run) {
    return <MissingRun />
  }

  return (
    <div className="flex h-full flex-col">
      <RunHeader
        run={run}
        subtitle={run.title?.trim() && run.task.trim() ? run.task.trim() : run.branch}
      />
      <RunTabs runID={run.id} active="run" />
      <div {...runTabPanel('run', 'min-h-0 flex-1 overflow-y-auto', true)}>
        <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 p-4 text-sm">
          {run.reason && (
            <>
              <dt className="text-muted-foreground">Reason</dt>
              <dd>{run.reason}</dd>
            </>
          )}
          <dt className="text-muted-foreground">Agent</dt>
          <dd>
            {run.harness} ({run.mode})
          </dd>
          <dt className="text-muted-foreground">Owner</dt>
          <dd style={{ color: owner?.color }}>
            {owner?.display_name ?? run.member_id}
          </dd>
          {run.account_member_id && run.account_member_id !== run.member_id && (
            <>
              <dt className="text-muted-foreground">Agent account</dt>
              <dd style={{ color: account?.color }}>
                {account?.display_name ?? run.account_member_id}
              </dd>
            </>
          )}
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
    </div>
  )
}

registerRoute('run', RunView)
