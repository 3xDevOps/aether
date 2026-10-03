import { Archive, GitCommitHorizontal, Shield } from 'lucide-react'
import { RunActions } from '@/components/run-actions'
import { StateIndicator } from '@/components/state-dot'
import { deletesInLabel, timeAgo } from '@/lib/format'
import { runLabel, runState, stateLabel, type PresentationState } from '@/lib/status'
import { focusRing } from '@/lib/utils'
import { MemberAvatar } from '@/routes/board/member-avatar'
import { RunTabs } from '@/routes/terminal/tabs'
import { useStore } from '@/store'
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

/** Shared title, state, task and metadata for every run-detail tab. */
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
  const owner = useStore((s) => s.members[run.member_id])
  const account = useStore((s) =>
    run.account_member_id ? s.members[run.account_member_id] : undefined,
  )
  const state = runState(run.status, pending.has(run.id))
  const label = runLabel(run)
  const task = run.task.trim()
  const detail = subtitle?.trim()
  const showDetail = Boolean(detail && detail !== task)
  const deletesLabel =
    run.archived_at && run.deletes_at ? deletesInLabel(run.deletes_at) : ''

  return (
    <div className="@container/run-header min-w-0 shrink-0">
      <header className="min-w-0 border-b border-border/80">
        <div className="grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-start gap-x-2 gap-y-1 px-3 py-1 sm:px-4">
          <div className="col-span-2 row-start-1 flex min-w-0 items-start gap-2">
            <h1
              className="line-clamp-2 min-w-0 flex-1 break-words text-[15px] font-semibold leading-5 text-foreground"
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
            {deletesLabel && (
              <span
                title="Archived"
                className="flex shrink-0 items-center gap-1 pt-0.5 text-xs text-muted-foreground"
              >
                <Archive className="size-3.5" aria-hidden />
                {deletesLabel}
              </span>
            )}
          </div>
          <div className="col-start-1 row-start-2 flex min-w-0 items-center gap-2 overflow-x-auto text-xs leading-4 text-muted-foreground [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
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
          <details className="contents">
            <summary
              className={`${focusRing} col-start-2 row-start-2 cursor-pointer whitespace-nowrap text-[11px] leading-5 text-muted-foreground underline underline-offset-2`}
            >
              Task and details
            </summary>
            <div
              tabIndex={0}
              className={`${focusRing} col-span-2 row-start-3 max-h-60 min-w-0 max-w-full space-y-2 overflow-y-auto border border-border/70 bg-muted/20 px-2 py-1.5`}
            >
              {task && (
                <p className="whitespace-pre-wrap break-words select-text text-[13px] leading-5 text-foreground/90">
                  {run.task}
                </p>
              )}
              <dl className="divide-y divide-border/70 select-text">
                <div className="grid min-w-0 gap-1 py-2 sm:grid-cols-[8rem_minmax(0,1fr)] sm:gap-4">
                  <dt className="text-xs font-medium text-muted-foreground">Owner</dt>
                  <dd className="flex min-w-0 items-center gap-2 break-words text-[13px] text-foreground">
                    <MemberAvatar member={owner} fallback={run.member_id} className="size-5 text-[9px]" />
                    <span className="min-w-0 break-words">{owner?.display_name ?? run.member_id}</span>
                  </dd>
                </div>
                {run.account_member_id && run.account_member_id !== run.member_id && (
                  <div className="grid min-w-0 gap-1 py-2 sm:grid-cols-[8rem_minmax(0,1fr)] sm:gap-4">
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
                <div className="grid min-w-0 gap-1 py-2 sm:grid-cols-[8rem_minmax(0,1fr)] sm:gap-4">
                  <dt className="text-xs font-medium text-muted-foreground">Created</dt>
                  <dd className="min-w-0 break-words text-[13px]">{timeAgo(run.created_at)}</dd>
                </div>
                <div className="grid min-w-0 gap-1 py-2 sm:grid-cols-[8rem_minmax(0,1fr)] sm:gap-4">
                  <dt className="text-xs font-medium text-muted-foreground">Changed</dt>
                  <dd className="min-w-0 break-words text-[13px]">{timeAgo(run.stateChangedAt)}</dd>
                </div>
                {run.last_commit_at && (
                  <div className="grid min-w-0 gap-1 py-2 sm:grid-cols-[8rem_minmax(0,1fr)] sm:gap-4">
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
            </div>
          </details>
          {run.reason && (
            <div
              tabIndex={0}
              className={`${focusRing} col-span-2 max-h-24 min-w-0 max-w-full overflow-y-auto border-l-2 px-2 py-1 text-[13px] leading-5 text-foreground/90 ${reasonTone(state)}`}
            >
              <span className="mr-1.5 font-medium text-muted-foreground">Reason</span>
              <span className="whitespace-pre-wrap break-words select-text">{run.reason}</span>
            </div>
          )}
        </div>
        <div className="flex min-h-9 min-w-0 items-stretch justify-between overflow-hidden border-t border-border bg-sidebar">
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
