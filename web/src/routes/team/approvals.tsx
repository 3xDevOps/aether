import { Check, ShieldQuestion, X } from 'lucide-react'
import { useEffect, useState } from 'react'
import type { CardSlotProps } from '@/components/slots'
import { Button } from '@/components/ui/button'
import { Chip } from '@/components/ui/heroui'
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
    <button
      type="button"
      onClick={() => navigate('approvals')}
      title={error ?? 'Open Approvals'}
      className={cn(
        focusRing,
        'flex items-center gap-1 rounded-md px-1.5 py-0.5 hover:bg-accent hover:text-foreground',
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
  )
}

/** A run card's marker: this run is holding somebody up. */
export function ApprovalBadge({ run }: CardSlotProps) {
  const inbox = useStore((s) => s.inbox)
  const navigate = useStore((s) => s.navigate)
  const waiting = approvalsForRun(inbox, run.id).length
  if (waiting === 0) return null

  return (
    <button
      type="button"
      onClick={() => navigate('approvals')}
      title={`${waiting} waiting on a decision`}
      className={cn(
        focusRing,
        'flex shrink-0 items-center gap-1 rounded-md px-1.5 py-0.5 hover:bg-state-needs-attention/20',
      )}
    >
      <ShieldQuestion className="size-3.5 text-state-needs-attention" aria-hidden />
      <Chip color="warning" variant="soft" size="sm">
        <Chip.Label>{waiting}</Chip.Label>
      </Chip>
    </button>
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
      <div className="flex shrink-0 flex-wrap items-center justify-between gap-2 border-b bg-muted/20 px-4 py-2">
        <div>
          <p className="text-sm font-medium">Decision queue</p>
          <p className="text-[13px] text-muted-foreground">
            Review requests before an agent continues.
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          aria-pressed={showDecided}
          onClick={() => setShowDecided(!showDecided)}
        >
          {showDecided ? 'Hide decided' : 'Show decided'}
        </Button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-4">
        <div className="mx-auto w-full max-w-4xl">
          {error && (
            <p
              role="alert"
              className="mb-3 rounded-md border border-state-failed/30 bg-state-failed/10 px-3 py-2 text-sm text-state-failed"
            >
              {error}
            </p>
          )}
          <ul className="space-y-2">
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
            <div className="rounded-md border border-dashed px-4 py-10 text-center">
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
        'rounded-lg border bg-card p-4 shadow-xs',
        !open && 'opacity-80',
      )}
    >
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
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
            <span className="text-sm font-medium break-words">{approval.action}</span>
          </div>
          {approval.detail ? (
            <div className="mt-3 rounded-md bg-muted/35 px-3 py-2">
              <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                Reason
              </p>
              <p className="mt-1 whitespace-pre-wrap text-sm leading-5">{approval.detail}</p>
            </div>
          ) : (
            <p className="mt-2 text-[13px] text-muted-foreground">No additional reason provided.</p>
          )}
        </div>
        {open && (
          <span className="flex shrink-0 gap-2 sm:pt-0.5">
            <Button size="sm" disabled={busy} onClick={() => void decide(true)}>
              <Check />
              Approve
            </Button>
            <Button
              size="sm"
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

      <div className="mt-3 flex flex-wrap items-center gap-x-2 gap-y-1 border-t pt-2 text-[13px] text-muted-foreground">
        <span className="font-medium text-foreground/80">
          {workspace?.name ?? approval.workspace_id}
        </span>
        {run && (
          <button
            type="button"
            onClick={() => navigate('terminal', { runId: run.id })}
            className={cn(
              focusRing,
              'max-w-full truncate hover:text-foreground hover:underline sm:max-w-60',
            )}
          >
            {runLabel(run)}
          </button>
        )}
        <time className="sm:ml-auto">{timeAgo(approval.created_at)}</time>
      </div>

      {!open && (
        <p className="mt-2 flex flex-wrap items-center gap-1.5 text-[13px]">
          <span
            aria-hidden
            className="size-2 shrink-0 rounded-full"
            style={{ backgroundColor: decider?.color }}
          />
          {approval.decision === 'approved' ? 'Approved' : 'Denied'} by{' '}
          {decider?.display_name ?? approval.decided_by ?? 'someone'}
          {approval.decided_at && ` ${timeAgo(approval.decided_at)}`}
        </p>
      )}
      {error && (
        <p role="alert" className="mt-2 rounded-md bg-state-failed/10 px-3 py-2 text-sm text-state-failed">
          {error}
        </p>
      )}
    </li>
  )
}
