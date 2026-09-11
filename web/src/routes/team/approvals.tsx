import { Check, ShieldQuestion, X } from 'lucide-react'
import { useEffect, useState } from 'react'
import type { CardSlotProps } from '@/components/slots'
import { Button } from '@/components/ui/button'
import { Chip, Tooltip } from '@/components/ui/heroui'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { timeAgo } from '@/lib/format'
import { runLabel } from '@/lib/status'
import { cn, focusRing } from '@/lib/utils'
import type { Approval } from '@/lib/types'
import type { RouteProps } from '@/routes/registry'
import { refreshInbox } from '@/routes/team/sync'
import { useStore } from '@/store'
import { approvalsForRun, pendingApprovals, sortByCreated } from '@/store/approvals'
/** The queue's size, in the status bar. Absent while nothing is waiting. */
export function ApprovalStatus() {
  const inbox = useStore((s) => s.inbox)
  const error = useStore((s) => s.inboxError)
  const navigate = useStore((s) => s.navigate)
  const waiting = pendingApprovals(inbox).length
  if (waiting === 0 && !error) return null

  return (
    <Tooltip>
      <Tooltip.Trigger<'button'>
        render={(triggerProps) => (
          <button
            {...triggerProps}
            type="button"
            onClick={() => {
              navigate('approvals')
            }}
            className={cn(
              focusRing,
              'flex h-[22px] min-h-[22px] coarse:h-11 coarse:min-h-11 shrink-0 items-center gap-1 px-1.5 text-xs hover:bg-toolbar-hover hover:text-foreground',
            )}
          >
            <ShieldQuestion
              className={cn(
                'size-3.5',
                error ? 'text-state-failed' : 'text-state-needs-attention',
              )}
              aria-hidden
            />
            <Chip
              color={error ? 'danger' : 'warning'}
              variant="soft"
              size="sm"
              className="max-w-44"
            >
              <Chip.Label className="truncate">
                {error ? 'queue unreadable' : `${waiting} waiting`}
              </Chip.Label>
            </Chip>
          </button>
        )}
      />
      <Tooltip.Content>{error ?? 'Open Approvals'}</Tooltip.Content>
    </Tooltip>
  )
}

/** A run card's marker: this run is holding somebody up. */
export function ApprovalBadge({ run }: CardSlotProps) {
  const inbox = useStore((s) => s.inbox)
  const navigate = useStore((s) => s.navigate)
  const waiting = approvalsForRun(inbox, run.id).length
  if (waiting === 0) return null

  return (
    <Tooltip>
      <Tooltip.Trigger<'button'>
        render={(triggerProps) => (
          <button
            {...triggerProps}
            type="button"
            onClick={() => {
              navigate('approvals')
            }}
            className={cn(
              focusRing,
              'flex h-[22px] min-h-[22px] coarse:h-11 coarse:min-h-11 shrink-0 items-center gap-1 px-1.5 py-0.5 hover:bg-state-needs-attention/20',
            )}
          >
            <ShieldQuestion className="size-3.5 text-state-needs-attention" aria-hidden />
            <Chip color="warning" variant="soft" size="sm">
              <Chip.Label>{waiting}</Chip.Label>
            </Chip>
          </button>
        )}
      />
      <Tooltip.Content>{`${waiting} waiting on a decision`}</Tooltip.Content>
    </Tooltip>
  )
}


/**
 * The shared inbox: every workspace's pending permission requests and plan
 * pauses in one queue. Decisions go through `approval.decide`, so the
 * server attributes them and the refusal a member without steer gets is the
 * server's, never the form's.
 */
export function ApprovalInbox({ client = api }: RouteProps & { client?: Api }) {
  const inbox = useStore((s) => s.inbox)
  const error = useStore((s) => s.inboxError)
  const showDecided = useStore((s) => s.showDecided)
  const setShowDecided = useStore((s) => s.setShowDecided)
  const [decisions, setDecisions] = useState<Record<string, Approval>>({})
  const waiting = pendingApprovals(inbox).length

  // The background refresh runs on the event cursor. This is the view that
  // claims to show the whole queue, so opening it reads it directly.
  useEffect(() => {
    void refreshInbox(useStore, client)
  }, [client, showDecided])

  // The queue as the last fetch saw it, with our own decisions laid over the
  // top: a request we just decided reports its outcome instead of vanishing
  // the moment we click, even though the next fetch no longer returns it.
  const byID = new Map(Object.values(inbox).flat().map((a) => [a.id, a]))
  for (const done of Object.values(decisions)) byID.set(done.id, done)
  const rows = sortByCreated([...byID.values()]).filter(
    (a) => a.decision === 'requested' || showDecided || decisions[a.id],
  )

  return (
    <div className="flex h-full min-h-0 flex-col">
      <ViewHeader
        title="Approvals"
        subtitle={waiting === 1 ? '1 request waiting' : `${waiting} requests waiting`}
      />
      <div className="flex min-h-[35px] shrink-0 flex-wrap items-center justify-between gap-2 border-b bg-sidebar px-3 py-1.5 sm:px-4">
        <div className="min-w-0">
          <p className="text-[13px] font-medium">Decision queue</p>
          <p className="text-xs text-muted-foreground">
            Review requests before an agent continues.
          </p>
        </div>
        <Button
          variant="outline"
          size="default"
          aria-pressed={showDecided}
          onClick={() => setShowDecided(!showDecided)}
        >
          {showDecided ? 'Hide decided' : 'Show decided'}
        </Button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-3 sm:p-4">
        <div className="mx-auto w-full max-w-5xl">
          {error && (
            <p
              role="alert"
              className="mb-3 border-l-2 border-state-failed bg-state-failed/10 px-3 py-2 text-[13px] text-state-failed"
            >
              {error}
            </p>
          )}
          <ul className="overflow-hidden border-y border-border">
            {rows.map((approval) => (
              <Row
                key={approval.id}
                approval={approval}
                client={client}
                onDecided={(done) =>
                  setDecisions((prev) => ({ ...prev, [done.id]: done }))
                }
              />
            ))}
          </ul>
          {rows.length === 0 && !error && (
            <div className="border-y border-dashed px-4 py-8 text-center">
              <ShieldQuestion className="mx-auto mb-2 size-5 text-muted-foreground" aria-hidden />
              <p className="text-sm font-medium">Nothing is waiting on a decision.</p>
              <p className="mt-1 text-[13px] text-muted-foreground">
                Requests will appear here when an agent needs your approval.
              </p>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

function Row({
  approval,
  client,
  onDecided,
}: {
  approval: Approval
  client: Api
  onDecided: (approval: Approval) => void
}) {
  const workspace = useStore((s) => s.workspaces[approval.workspace_id])
  const run = useStore((s) => s.runs[approval.run_id])
  const decider = useStore((s) =>
    approval.decided_by ? s.members[approval.decided_by] : undefined,
  )
  const navigate = useStore((s) => s.navigate)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const open = approval.decision === 'requested'

  const decide = async (approve: boolean) => {
    setBusy(true)
    setError(null)
    try {
      onDecided(await client.approvalDecide(approval.run_id, approval.id, approve))
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <li
      className={cn(
        'border-b border-border px-3 py-3 last:border-b-0',
        !open && 'bg-muted/10',
      )}
    >
      <div className="grid min-w-0 gap-2 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-start">
        <div className="min-w-0">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <Chip
              color={open ? 'warning' : approval.decision === 'approved' ? 'success' : 'danger'}
              variant="soft"
              size="sm"
            >
              <Chip.Label>
                {open
                  ? 'Needs decision'
                  : approval.decision === 'approved'
                    ? 'Approved'
                    : 'Denied'}
              </Chip.Label>
            </Chip>
            <span className="min-w-0 break-words text-[13px] font-medium">{approval.action}</span>
          </div>
          {approval.detail ? (
            <div className="mt-2 border-l-2 border-border pl-2">
              <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                Reason
              </p>
              <p className="mt-1 whitespace-pre-wrap break-words text-[13px] leading-5 select-text">
                {approval.detail}
              </p>
            </div>
          ) : (
            <p className="mt-2 text-[13px] text-muted-foreground">No additional reason provided.</p>
          )}
        </div>
        {open && (
          <span className="flex flex-wrap items-center gap-1.5 sm:justify-end">
            <Button size="default" disabled={busy} onClick={() => void decide(true)}>
              <Check />
              Approve
            </Button>
            <Button
              size="default"
              variant="outline"
              disabled={busy}
              onClick={() => void decide(false)}
            >
              <X />
              Deny
            </Button>
          </span>
        )}
      </div>

      <div className="mt-2 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-[13px] text-muted-foreground">
        <span className="min-w-0 break-words font-medium text-foreground/80">
          {workspace?.name ?? approval.workspace_id}
        </span>
        {run && (
          <button
            type="button"
            onClick={() => navigate('terminal', { runId: run.id })}
            title={runLabel(run)}
            className={cn(
              focusRing,
              'inline-flex min-h-[26px] coarse:min-h-11 min-w-0 max-w-full items-center truncate text-left hover:text-foreground hover:underline sm:max-w-60',
            )}
          >
            {runLabel(run)}
          </button>
        )}
        <time className="sm:ml-auto">{timeAgo(approval.created_at)}</time>
      </div>

      {!open && (
        <p className="mt-1.5 flex min-w-0 flex-wrap items-center gap-1.5 text-[13px]">
          <span
            aria-hidden
            className="size-2 shrink-0 rounded-full"
            style={{ backgroundColor: decider?.color }}
          />
          {approval.decision === 'approved' ? 'Approved' : 'Denied'} by{' '}
          <span className="min-w-0 break-words">{decider?.display_name ?? approval.decided_by ?? 'someone'}</span>
          {approval.decided_at && ` ${timeAgo(approval.decided_at)}`}
        </p>
      )}
      {error && (
        <p role="alert" className="mt-2 border-l-2 border-state-failed bg-state-failed/10 px-2 py-1.5 text-[13px] text-state-failed">
          {error}
        </p>
      )}
    </li>
  )
}
