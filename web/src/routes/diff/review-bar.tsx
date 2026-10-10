import { useRef, useState } from 'react'
import { Check, MessageSquare, X } from '@/components/icons'
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
import { canReopenRun } from '@/lib/commands'
import { message } from '@/lib/format'
import { allowed } from '@/lib/permissions'
import { sendToAgent } from '@/lib/send-to-agent'
import type { RoomMessage } from '@/lib/types'
import { cn } from '@/lib/utils'
import type { PatchFile } from '@/routes/diff/parse'
import { patchReview, review, reviewEntries, reviewMessage, useReview, written } from '@/routes/diff/review'
import { modeLabel } from '@/routes/run/agent-name'
import { useImplicitControl, type AgentTerminal } from '@/routes/run/agent-terminal'
import { composerBlock } from '@/routes/run/composer-state'
import { useStore } from '@/store'
import { useCapability, useHeaderPrimary, useSelf } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

function plural(count: number): string {
  return count === 1 ? '1 comment' : `${count} comments`
}

/** Why a send that the server answered left the comments in place. */
function refusal(sent: RoomMessage): string {
  if (sent.state === 'uncertain') return 'Delivery uncertain: the message may have reached the agent. Check the session before sending again.'
  return `Not sent: ${sent.failure?.message ?? sent.state}.`
}

function receipt(sent: RoomMessage, count: number, agentName: string): { text: string; tone?: 'done' | 'failed' } {
  const what = plural(count)
  switch (sent.state) {
    case 'queued':
      return { text: `${what} queued for ${agentName}. Delivers in 45 s unless the controller decides sooner.` }
    case 'not_sent':
      return { text: `${agentName} did not get your ${what}: ${sent.failure?.message ?? 'not sent'}. The text is in the session.`, tone: 'failed' }
    case 'uncertain':
      return { text: `Your ${what} may not have reached ${agentName}. The text is in the session.`, tone: 'failed' }
    case 'denied':
      return { text: `The controller declined your ${what}. The text is in the session.`, tone: 'failed' }
    case 'cancelled':
      return { text: `Delivery of your ${what} was cancelled. The text is in the session.`, tone: 'failed' }
    default:
      return { text: `Sent ${what} to ${agentName}${sent.agent_delivery === 'queued' ? ', queued behind its current turn' : ''}.`, tone: 'done' }
  }
}

/** The review in progress: how many comments wait, sending them as one message, and what became of the last send. */
export function ReviewBar({
  run,
  agent,
  agentName,
  files,
  scope,
}: {
  run: RunRecord
  agent: AgentTerminal
  agentName: string
  /** The diff on screen, which decides each comment's line numbers and whether it is outdated. */
  files: PatchFile[]
  scope: string
}) {
  const self = useSelf()
  const cap = useCapability()
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const { comments, sending, error, sent } = useReview(run.id)
  // The store's copy is the live one: a queued message is later sent, denied or refused.
  const delivered = useStore((s) => (sent ? s.roomMessages[run.id]?.find((entry) => entry.id === sent.message.id) : undefined))
  const control = useImplicitControl(run, agent)
  const [discarding, setDiscarding] = useState(false)
  const latest = useRef({ files, scope, held: agent.roomControl })
  latest.current = { files, scope, held: agent.roomControl }
  const pinned = comments.filter((comment) => comment.body).length
  useHeaderPrimary(pinned > 0)

  const maySteer = allowed('steer', self, { owner: run.member_id, protected: run.protected, steerOthers })
  const blocked = run.switching
    ? `Switching to ${modeLabel[run.switching] ?? run.switching}…`
    : run.acp && run.mode === 'headless'
      ? 'Background runs take no input.'
      : composerBlock(run, maySteer, maySteer && cap.hasMethod('run.relaunch') && canReopenRun(run))

  const deliver = async () => {
    const { files: shown, scope: at, held } = latest.current
    const entries = reviewEntries(review(run.id).comments, shown, at)
    if (review(run.id).sending || entries.length === 0) return
    patchReview(run.id, { sending: true, error: undefined })
    try {
      const lease = held?.has_control && held.control_generation > 0
        ? { control_session_id: held.control_session_id, control_generation: held.control_generation }
        : undefined
      const posted = await sendToAgent(run, reviewMessage(entries), lease)
      if (posted.state !== 'sent' && posted.state !== 'queued') {
        patchReview(run.id, { sending: false, error: refusal(posted) })
        return
      }
      const gone = new Set(entries.map((entry) => entry.comment.id))
      patchReview(run.id, {
        comments: review(run.id).comments.filter((comment) => !gone.has(comment.id)),
        sending: false,
        sent: { message: posted, count: entries.length },
      })
    } catch (cause) {
      patchReview(run.id, { sending: false, error: message(cause) })
    }
  }
  const send = () => {
    if (control.canAct) control.withControl(() => void deliver())
    else void deliver()
  }

  if (pinned === 0) {
    if (!sent) return null
    const { text, tone } = receipt(delivered ?? sent.message, sent.count, agentName)
    return (
      <div role="group" aria-label="Review" className="flex min-h-8 min-w-0 shrink-0 flex-wrap items-center gap-x-2 border-b border-seam bg-chrome px-3 py-0.5 text-ui-sm">
        {tone === 'done' && <Check aria-hidden className="size-3.5 shrink-0 text-state-done" />}
        <p role="status" className={cn('min-w-0 flex-1', tone === 'failed' ? 'text-state-failed' : 'text-text')}>
          {text}{' '}
          <Button variant="link" size="sm" onClick={() => useStore.getState().navigate('run', { runId: run.id, view: 'session' })}>
            Open the session
          </Button>
        </p>
        <Button variant="ghost" size="icon-sm" label="Dismiss" onClick={() => patchReview(run.id, { sent: undefined })}>
          <X />
        </Button>
      </div>
    )
  }

  const count = written(comments).length
  const note = blocked ?? (control.canAct ? '' : 'Delivers in 45 s unless the controller decides sooner.')
  return (
    <div role="group" aria-label="Review" className="flex min-h-8 min-w-0 shrink-0 flex-wrap items-center gap-x-2 gap-y-0.5 border-b border-seam bg-chrome px-3 py-0.5 text-ui-sm">
      <span className="flex shrink-0 items-center gap-1.5 font-medium text-text tabular-nums">
        <MessageSquare aria-hidden className="size-3.5 text-muted" />
        {plural(count)}
      </span>
      {error ? (
        <p role="alert" className="min-w-0 flex-1 basis-40 break-words text-state-failed">{error}</p>
      ) : note && (
        <p className="min-w-0 flex-1 basis-40 text-muted">{note}</p>
      )}
      <span className="ml-auto flex shrink-0 items-center gap-1">
        <Button variant="ghost" size="sm" disabled={sending} onClick={() => setDiscarding(true)}>
          Discard
        </Button>
        <Button size="sm" disabled={sending || blocked !== null} onClick={send}>
          {sending ? 'Sending…' : 'Send to agent'}
        </Button>
      </span>
      <AlertDialog open={discarding} onOpenChange={setDiscarding}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Discard {plural(count)}?</AlertDialogTitle>
            <AlertDialogDescription>They have not been sent to the agent.</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep</AlertDialogCancel>
            <AlertDialogAction onClick={() => patchReview(run.id, { comments: [], error: undefined })}>Discard</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
