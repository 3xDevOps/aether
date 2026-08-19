import { Check, ShieldQuestion, X } from 'lucide-react'
import { useEffect, useState } from 'react'
import type { CardSlotProps } from '@/components/slots'
import { Button } from '@/components/ui/button'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { timeAgo } from '@/lib/format'
import { cn } from '@/lib/utils'
import type { Approval } from '@/lib/types'
import type { RouteProps } from '@/routes/registry'
import { refreshInbox } from '@/routes/team/sync'
import { useStore } from '@/store'
import { approvalsForRun, pendingApprovals, sortByCreated } from '@/store/approvals'

/** The queue's size, in the status bar. Absent while nothing is waiting. */
export function ApprovalStatus() {
  const inbox = useStore((s) => s.inbox)
  const navigate = useStore((s) => s.navigate)
  const waiting = pendingApprovals(inbox).length
  if (waiting === 0) return null

  return (
    <button
      type="button"
      onClick={() => navigate('approvals')}
      title="Open the approval inbox"
      className="flex items-center gap-1 rounded px-1 text-state-needs-attention hover:underline"
    >
      <ShieldQuestion className="size-3.5" aria-hidden />
      {waiting} waiting
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
      className="flex shrink-0 items-center gap-1 rounded-sm bg-state-needs-attention/15 px-1 text-[11px] text-state-needs-attention"
    >
      <ShieldQuestion className="size-3.5" aria-hidden />
      {waiting}
    </button>
  )
}

/**
 * The shared inbox: every session's pending permission requests and plan
 * pauses in one queue. Decisions go through `approval.decide`, so the
 * server attributes them and the refusal a member without steer gets is the
 * server's, never the form's.
 */
export function ApprovalInbox({ client = api }: RouteProps & { client?: Api }) {
  const inbox = useStore((s) => s.inbox)
  const showDecided = useStore((s) => s.showDecided)
  const setShowDecided = useStore((s) => s.setShowDecided)
  const [decisions, setDecisions] = useState<Record<string, Approval>>({})
  const waiting = pendingApprovals(inbox).length

  // The background refresh only covers live sessions. This is the view that
  // claims to show the whole queue, so opening it reads them all.
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
    <div className="flex h-full flex-col">
      <ViewHeader
        title="Approval inbox"
        subtitle={waiting === 1 ? '1 request waiting' : `${waiting} requests waiting`}
      />
      <div className="flex items-center border-b px-4 py-1">
        <Button
          variant="ghost"
          size="sm"
          aria-pressed={showDecided}
          onClick={() => setShowDecided(!showDecided)}
        >
          {showDecided ? 'Hide decided' : 'Show decided'}
        </Button>
      </div>
      <div className="flex-1 overflow-y-auto p-3">
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
        {rows.length === 0 && (
          <p className="text-sm text-muted-foreground">
            Nothing is waiting on a decision.
          </p>
        )}
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
  const session = useStore((s) => s.sessions[approval.session_id])
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
    <li className={cn('rounded-md border bg-card p-2', !open && 'opacity-80')}>
      <div className="flex items-start gap-2">
        <span className="min-w-0 flex-1">
          <span className="text-sm font-medium break-words">{approval.action}</span>
          {approval.detail && (
            <span className="mt-1 block text-xs whitespace-pre-wrap text-muted-foreground">
              {approval.detail}
            </span>
          )}
        </span>
        {open && (
          <span className="flex shrink-0 gap-1">
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

      <div className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
        <span>{session?.name ?? approval.session_id}</span>
        {run && (
          <button
            type="button"
            onClick={() => navigate('run', { runId: run.id })}
            className="max-w-60 truncate hover:text-foreground hover:underline"
          >
            {run.task}
          </button>
        )}
        <time className="ml-auto">{timeAgo(approval.created_at)}</time>
      </div>

      {!open && (
        <p className="mt-1 flex items-center gap-1.5 text-xs">
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
      {error && <p className="mt-1 text-xs text-state-failed">{error}</p>}
    </li>
  )
}
