import { useState } from 'react'
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
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { cn } from '@/lib/utils'
import { receipt, refusal, useAgentSend } from '@/routes/diff/agent-send'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { useStore } from '@/store'
import { useCapability, useHeaderPrimary } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import { feedback, feedbackMessage, logKey, noteText, patchFeedback, summary, updateNote, useFeedback, written, type Feedback, type Note } from './feedback'

/** The room refuses a body over 64 KiB; this leaves room for multi-byte text. */
const maxMessageBytes = 60_000

let sends = 0

/**
 * What waits to go to the run's agent from the Browser, and the one action
 * that sends it: the Changes view's review bar, for notes on a page.
 */
export function FeedbackBar({ run, agent, agentName }: { run: RunRecord; agent: AgentTerminal; agentName: string }) {
  const { notes, logs, sending, error, sent } = useFeedback(run.id)
  // The store's copy is the live one: a queued message is later sent, denied or refused.
  const delivered = useStore((s) => (sent ? s.roomMessages[run.id]?.find((entry) => entry.id === sent.message.id) : undefined))
  const agentSend = useAgentSend(run, agent)
  const cap = useCapability()
  const [discarding, setDiscarding] = useState(false)
  const confirmed = notes.filter((note) => note.kind === 'screenshot' || note.body).length + logs.length
  useHeaderPrimary(confirmed > 0)

  const deliver = async () => {
    const taken = feedback(run.id)
    const carried = written(taken.notes)
    if (taken.sending || carried.length + taken.logs.length === 0) return
    const mark = ++sends
    patchFeedback(run.id, { sending: mark, error: undefined })
    // Feedback dropped meanwhile, by a sign-in or a deleted run, has a successor this must not write to.
    const dropped = () => feedback(run.id).sending !== mark
    const settle = (patch: Partial<Feedback>) => {
      if (feedback(run.id).sending === mark) patchFeedback(run.id, { ...patch, sending: undefined })
    }
    try {
      const pictured: Note[] = []
      for (const note of carried) {
        if (note.kind !== 'element' || !note.image || note.path || !cap.hasMethod('terminal.image')) {
          pictured.push(note)
          continue
        }
        let path: string
        try {
          ({ path } = await api.uploadTerminalImage(new File([note.image], 'annotation.png', { type: 'image/png' }), run.id))
        } catch (cause) {
          return settle({ error: `Image upload failed: ${message(cause)}` })
        }
        if (dropped()) return
        // Kept on the note, so a send that is retried names the same file and stays one message.
        updateNote(run.id, note.id, { path })
        pictured.push({ ...note, path })
      }
      if (dropped()) return
      const body = feedbackMessage(pictured, taken.logs)
      if (new TextEncoder().encode(body).length > maxMessageBytes) {
        return settle({ error: 'This is more than one message carries (64 KiB). Send fewer notes or rows at a time.' })
      }
      const posted = await agentSend.post(body)
      if (posted.state !== 'sent' && posted.state !== 'queued') return settle({ error: refusal(posted) })
      const said = new Map(pictured.map((note) => [note.id, note.body]))
      const rows = new Set(taken.logs.map(logKey))
      const now = feedback(run.id)
      settle({
        // Only what the agent got goes: not a note added or reworded while the request was out.
        notes: now.notes.filter((note) => said.get(note.id) !== noteText(note)),
        logs: now.logs.filter((entry) => !rows.has(logKey(entry))),
        sent: { message: posted, what: summary(pictured, taken.logs) },
      })
    } catch (cause) {
      settle({ error: message(cause) })
    }
  }

  const bar = 'flex min-h-8 min-w-0 shrink-0 flex-wrap items-center gap-x-2 gap-y-0.5 border-b border-seam bg-chrome px-3 py-0.5 text-ui-sm'
  if (confirmed === 0) {
    if (!sent) return null
    const { text, tone } = receipt(delivered ?? sent.message, sent.what, agentName)
    return (
      <div role="group" aria-label="Feedback for the agent" className={bar}>
        {tone === 'done' && <Check aria-hidden className="size-3.5 shrink-0 text-state-done" />}
        <p role="status" className={cn('min-w-0 flex-1', tone === 'failed' ? 'text-state-failed' : 'text-text')}>
          {text}{' '}
          <Button variant="link" size="sm" onClick={() => useStore.getState().navigate('run', { runId: run.id, view: 'session' })}>
            Open the session
          </Button>
        </p>
        <Button variant="ghost" size="icon-sm" label="Dismiss" onClick={() => patchFeedback(run.id, { sent: undefined })}>
          <X />
        </Button>
      </div>
    )
  }

  const what = summary(written(notes), logs)
  return (
    <div role="group" aria-label="Feedback for the agent" className={bar}>
      <span className="flex min-w-0 items-center gap-1.5 font-medium text-text tabular-nums">
        <MessageSquare aria-hidden className="size-3.5 shrink-0 text-muted" />
        <span className="min-w-0 break-words">{what}</span>
      </span>
      {error && <p role="alert" className="min-w-0 flex-1 basis-40 break-words text-state-failed">{error}</p>}
      {agentSend.note && <p className="min-w-0 flex-1 basis-40 text-muted">{agentSend.note}</p>}
      <span className="ml-auto flex shrink-0 items-center gap-1">
        <Button variant="ghost" size="sm" disabled={Boolean(sending)} onClick={() => setDiscarding(true)}>
          Discard
        </Button>
        <Button size="sm" disabled={Boolean(sending) || agentSend.blocked !== null} onClick={() => agentSend.dispatch(() => void deliver())}>
          {sending ? 'Sending…' : 'Send to agent'}
        </Button>
      </span>
      <AlertDialog open={discarding} onOpenChange={setDiscarding}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Discard {what}?</AlertDialogTitle>
            <AlertDialogDescription>Nothing here has been sent to the agent, and discarded notes cannot be brought back. A screenshot stays in the run's captures.</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep</AlertDialogCancel>
            <AlertDialogAction onClick={() => patchFeedback(run.id, { notes: [], logs: [], error: undefined })}>Discard</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
