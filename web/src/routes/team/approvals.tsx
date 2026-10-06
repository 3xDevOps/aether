import { Check, ShieldQuestion, X } from 'lucide-react'
import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { RelativeTime } from '@/components/ui/relative-time'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { runLabel } from '@/lib/status'
import { cn, focusRing } from '@/lib/utils'
import type { Approval } from '@/lib/types'
import type { RouteProps } from '@/routes/registry'
import { refreshInbox } from '@/routes/team/sync'
import { useStore } from '@/store'
import { pendingApprovals, sortByCreated } from '@/store/approvals'

/** Decisions go through `approval.decide`, so the server attributes them and owns the refusal for a member without steer. */
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

  // Our own decisions overlay the last fetch, so a request we just decided
  // shows its outcome instead of vanishing when the next fetch drops it.
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
        actions={
          <Button
            variant="secondary"
            size="md"
            aria-pressed={showDecided}
            onClick={() => setShowDecided(!showDecided)}
          >
            {showDecided ? 'Hide decided' : 'Show decided'}
          </Button>
        }
      />
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
            <Badge tone={open ? 'needs-you' : approval.decision === 'approved' ? 'done' : 'failed'}>
              {open ? 'Needs decision' : approval.decision === 'approved' ? 'Approved' : 'Denied'}
            </Badge>
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
            <Button size="md" disabled={busy} onClick={() => void decide(true)}>
              <Check />
              Approve
            </Button>
            <Button
              size="md"
              variant="secondary"
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
        <RelativeTime at={approval.created_at} className="sm:ml-auto" />
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
          {approval.decided_at && <> <RelativeTime at={approval.decided_at} /></>}
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
