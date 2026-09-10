import { GitCommitHorizontal, UserRound } from 'lucide-react'
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
    <div className="flex h-full min-h-0 flex-col">
      <RunHeader
        run={run}
        subtitle={run.title?.trim() && run.task.trim() ? run.task.trim() : run.branch}
      />
      <RunTabs runID={run.id} active="run" />
      <div {...runTabPanel('run', 'min-h-0 flex-1 overflow-y-auto', true)}>
        <main className="mx-auto w-full max-w-4xl space-y-5 p-4 md:p-6">
          <div>
            <p className="text-xs font-medium uppercase tracking-[0.14em] text-muted-foreground">
              Run overview
            </p>
            <h2 className="mt-1 text-lg font-semibold tracking-tight">Metadata and ownership</h2>
            <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
              Keep the run's context close while you review its terminal, files, and changes.
            </p>
          </div>
          <section aria-label="Run metadata" className="rounded-lg border bg-card">
            <dl className="grid gap-px bg-border sm:grid-cols-2">
              {run.reason && (
                <div className="bg-card p-4 sm:col-span-2">
                  <dt className="text-xs font-medium text-muted-foreground">Reason</dt>
                  <dd className="mt-1 text-sm leading-5">{run.reason}</dd>
                </div>
              )}
              <div className="bg-card p-4">
                <dt className="text-xs font-medium text-muted-foreground">Agent</dt>
                <dd className="mt-1 text-sm">{run.harness} ({run.mode})</dd>
              </div>
              <div className="bg-card p-4">
                <dt className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
                  <UserRound className="size-3.5" aria-hidden />
                  Owner
                </dt>
                <dd className="mt-1 text-sm" style={{ color: owner?.color }}>
                  {owner?.display_name ?? run.member_id}
                </dd>
              </div>
              {run.account_member_id && run.account_member_id !== run.member_id && (
                <div className="bg-card p-4">
                  <dt className="text-xs font-medium text-muted-foreground">Agent account</dt>
                  <dd className="mt-1 text-sm" style={{ color: account?.color }}>
                    {account?.display_name ?? run.account_member_id}
                  </dd>
                </div>
              )}
              <div className="bg-card p-4">
                <dt className="text-xs font-medium text-muted-foreground">Created</dt>
                <dd className="mt-1 text-sm">{timeAgo(run.created_at)}</dd>
              </div>
              <div className="bg-card p-4">
                <dt className="text-xs font-medium text-muted-foreground">Changed</dt>
                <dd className="mt-1 text-sm">{timeAgo(run.stateChangedAt)}</dd>
              </div>
              {run.last_commit_at && (
                <div className="bg-card p-4 sm:col-span-2">
                  <dt className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
                    <GitCommitHorizontal className="size-3.5" aria-hidden />
                    Last commit
                  </dt>
                  <dd className="mt-1 text-sm">
                    <code className="font-mono text-[13px]" title={run.last_commit}>
                      {run.last_commit?.slice(0, 8)}
                    </code>{' '}
                    <span className="text-muted-foreground">{timeAgo(run.last_commit_at)}</span>
                  </dd>
                </div>
              )}
            </dl>
          </section>
        </main>
      </div>
    </div>
  )
}

registerRoute('run', RunView)
