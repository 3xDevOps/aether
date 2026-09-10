import { GitCommitHorizontal } from 'lucide-react'
import { MissingRun } from '@/components/missing-run'
import { MemberAvatar } from '@/routes/board/member-avatar'
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
      <RunHeader run={run} subtitle={run.branch} />
      <RunTabs runID={run.id} active="run" />
      <div {...runTabPanel('run', 'min-h-0 flex-1 overflow-y-auto', true)}>
        <main className="mx-auto w-full max-w-5xl min-w-0 space-y-3 p-3 sm:p-4">
          <header className="border-b border-border/80 pb-2">
            <div className="flex min-w-0 flex-wrap items-baseline gap-x-3 gap-y-0.5">
              <h2 className="text-[15px] font-semibold leading-5">Run overview</h2>
              <span className="text-xs text-muted-foreground">Metadata and ownership</span>
            </div>
            <p className="mt-0.5 max-w-2xl text-xs leading-4 text-muted-foreground">
              Keep the run&apos;s context close while you review its terminal, files, and changes.
            </p>
          </header>
          <section aria-label="Run metadata" className="min-w-0 border-y border-border/80">
            <dl className="divide-y divide-border/70">
              <div className="grid min-w-0 gap-1 px-3 py-2.5 sm:grid-cols-[8rem_minmax(0,1fr)] sm:gap-4">
                <dt className="text-xs font-medium text-muted-foreground">Owner</dt>
                <dd className="flex min-w-0 items-center gap-2 break-words text-[13px] text-foreground">
                  <MemberAvatar member={owner} fallback={run.member_id} className="size-5 text-[9px]" />
                  <span className="min-w-0 break-words">{owner?.display_name ?? run.member_id}</span>
                </dd>
              </div>
              {run.account_member_id && run.account_member_id !== run.member_id && (
                <div className="grid min-w-0 gap-1 px-3 py-2.5 sm:grid-cols-[8rem_minmax(0,1fr)] sm:gap-4">
                  <dt className="text-xs font-medium text-muted-foreground">Agent account</dt>
                  <dd className="flex min-w-0 items-center gap-2 break-words text-[13px] text-foreground">
                    <MemberAvatar
                      member={account}
                      fallback={run.account_member_id}
                      className="size-5 text-[9px]"
                    />
                    <span className="min-w-0 break-words">
                      {account?.display_name ?? run.account_member_id}
                    </span>
                  </dd>
                </div>
              )}
              <div className="grid min-w-0 gap-1 px-3 py-2.5 sm:grid-cols-[8rem_minmax(0,1fr)] sm:gap-4">
                <dt className="text-xs font-medium text-muted-foreground">Created</dt>
                <dd className="min-w-0 break-words text-[13px]">{timeAgo(run.created_at)}</dd>
              </div>
              <div className="grid min-w-0 gap-1 px-3 py-2.5 sm:grid-cols-[8rem_minmax(0,1fr)] sm:gap-4">
                <dt className="text-xs font-medium text-muted-foreground">Changed</dt>
                <dd className="min-w-0 break-words text-[13px]">{timeAgo(run.stateChangedAt)}</dd>
              </div>
              {run.last_commit_at && (
                <div className="grid min-w-0 gap-1 px-3 py-2.5 sm:grid-cols-[8rem_minmax(0,1fr)] sm:gap-4">
                  <dt className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
                    <GitCommitHorizontal className="size-3.5" aria-hidden />
                    Last commit
                  </dt>
                  <dd className="min-w-0 break-words text-[13px]">
                    <code className="font-mono text-[12px]" title={run.last_commit}>
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
