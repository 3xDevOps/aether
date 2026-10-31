import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { api } from '@/lib/api'
import { useClock } from '@/lib/clock'
import { clockTime, errorSentence, timeAgo } from '@/lib/format'
import { plainReason } from '@/lib/reasons'
import { possiblyStalled } from '@/lib/status'
import type { RunLiveness } from '@/lib/types'
import { useImplicitControl, type AgentTerminal } from '@/routes/run/agent-terminal'
import type { RunNavigation } from '@/routes/run/header'
import { useStore } from '@/store'
import type { RunRecord } from '@/store/runs'
import type { SessionRow } from '@/store/session-rows'
import { sessionLease } from '@/store/session-stream'

// Mirrors coordhooks.ReportReminderLead in internal/coordhooks/guide.go: Aether's prompt, not the member's.
const reportReminderLead = 'Report your state with aether-internal report'

function LastProgress({ liveness, now }: { liveness: RunLiveness; now: number }) {
  if (liveness.last_progress_at) {
    return <>{clockTime(liveness.last_progress_at, now)} ({timeAgo(liveness.last_progress_at, now)})</>
  }
  if (liveness.connected_at) return <>none since the agent connected at {clockTime(liveness.connected_at, now)}</>
  return <>none</>
}

export function StallCallout({ run, agent, rows, nav }: {
  run: RunRecord
  agent: AgentTerminal
  rows: SessionRow[]
  nav: RunNavigation
}) {
  const now = useClock()
  const stalled = possiblyStalled(run)
  const stallReason = stalled ? run.reason : undefined
  const [{ liveness, error }, setRead] = useState<{ liveness?: RunLiveness; error?: string }>({})
  useEffect(() => {
    if (stallReason === undefined) return
    let current = true
    api.runGet(run.id)
      .then((read) => { if (current) setRead({ liveness: read.liveness }) })
      .catch((err: unknown) => { if (current) setRead({ error: errorSentence(err) }) })
    return () => {
      current = false
    }
  }, [run.id, stallReason])
  const control = useImplicitControl(run, agent)
  const [busy, setBusy] = useState(false)
  // The prompt the dialog shows, so a newer one sent while it is open is not what gets resent.
  const [confirm, setConfirm] = useState<Extract<SessionRow, { kind: 'user' }>>()
  // Kept until the server answers for the prompt, so a retry after a lost reply cannot queue it twice.
  const resendKey = useRef<{ prompt: string; key: string }>(undefined)
  useEffect(() => {
    if (!stalled) setConfirm(undefined)
  }, [stalled])
  if (!stalled) return null
  const lastPrompt = [...rows].reverse().find((row): row is Extract<SessionRow, { kind: 'user' }> =>
    row.kind === 'user' && (row.delivery === undefined || row.delivery === 'Sent') && !row.body.startsWith(reportReminderLead))

  const attempt = async (what: string, act: () => Promise<unknown>) => {
    setBusy(true)
    try {
      await act()
    } catch (err) {
      toast.error(`${what} failed: ${errorSentence(err)}`)
    } finally {
      setBusy(false)
    }
  }
  const stop = () => control.withControl(() => {
    const lease = sessionLease(useStore, run.id)
    if (lease) void attempt('Stop turn', () => api.runACPCancel(run.id, lease))
  })
  const resend = () => {
    const { id, body, files } = confirm!
    if (resendKey.current?.prompt !== id) resendKey.current = { prompt: id, key: crypto.randomUUID() }
    const { key } = resendKey.current
    const send = () => void attempt('Resend', async () => {
      const result = await api.runInject(run.id, body, key, { lease: sessionLease(useStore, run.id), attachments: files })
      useStore.getState().upsertRoomMessage(result.message)
      const delivery = result.receipt === 'not_sent' || result.receipt === 'uncertain' ? result.receipt : result.message.state
      if (delivery !== 'uncertain') resendKey.current = undefined
      if (delivery === 'uncertain') throw new Error(result.message.failure?.message ?? 'Delivery is uncertain; check the conversation before resending')
      if (delivery === 'not_sent') throw new Error(result.message.failure?.message ?? 'The prompt was not sent')
    })
    if (control.canAct) control.withControl(send)
    else send()
  }

  return (
    <>
      <Callout
        tone="waiting"
        title="Possibly stalled"
        actions={
          <>
            <Button size="sm" variant="secondary" disabled={busy || !control.canAct} hint={control.canAct ? 'Cancels the agent’s turn. Costs nothing.' : 'Take control of the conversation to stop its turn'} onClick={stop}>
              Stop turn
            </Button>
            <Button size="sm" variant="secondary" hint="Opens the turn’s timeline of model calls and tools" onClick={() => nav.go('trace')}>
              Open trace
            </Button>
            {lastPrompt !== undefined && (
              <Button
                size="sm"
                variant="ghost"
                disabled={busy || Boolean(lastPrompt.images?.length)}
                hint={lastPrompt.images?.length ? 'Your last prompt had images; send it again from the composer' : 'Sends your last prompt again. Costs tokens.'}
                onClick={() => setConfirm(lastPrompt)}
              >
                Resend last prompt…
              </Button>
            )}
          </>
        }
      >
        <p>{plainReason(run.reason!)}.</p>
        {liveness && (
          <dl className="mt-1 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 text-ui-sm text-muted">
            <dt>Last progress</dt>
            <dd><LastProgress liveness={liveness} now={now} /></dd>
            {liveness.phase && (
              <>
                <dt>Phase</dt>
                <dd className="font-code">{liveness.phase}</dd>
              </>
            )}
            <dt>Connection</dt>
            <dd>{liveness.transport}</dd>
          </dl>
        )}
        {error && <p className="mt-1 text-ui-sm text-muted">Could not read the agent’s progress: {error}</p>}
        <p className="mt-1 text-ui-sm text-muted">Aether restarts nothing on its own. A slow model can still answer.</p>
      </Callout>
      <AlertDialog open={confirm !== undefined} onOpenChange={(open) => { if (!open) setConfirm(undefined) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Resend your last prompt?</AlertDialogTitle>
            <AlertDialogDescription>
              The agent gets your last prompt again as a new message. It costs tokens, and the agent may repeat tool
              actions it already took, such as commands and file edits. A turn that is still running finishes first;
              stop it to send now.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <pre className="max-h-32 overflow-y-auto rounded-control bg-chrome px-2 py-1 font-code text-ui-sm break-words whitespace-pre-wrap">{confirm?.body}</pre>
          {confirm?.files?.length ? <p className="text-ui-sm text-muted">With {confirm.files.length === 1 ? '1 attached file' : `${confirm.files.length} attached files`}.</p> : null}
          <AlertDialogFooter>
            <AlertDialogCancel>Keep waiting</AlertDialogCancel>
            <AlertDialogAction onClick={resend}>Resend and spend tokens</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}
