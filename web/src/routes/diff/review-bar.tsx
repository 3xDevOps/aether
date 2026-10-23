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
import { message } from '@/lib/format'
import { cn } from '@/lib/utils'
import { receipt, refusal, useAgentSend } from '@/routes/diff/agent-send'
import type { PatchFile } from '@/routes/diff/parse'
import { patchReview, review, reviewEntries, reviewMessage, useReview, written, type Review } from '@/routes/diff/review'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { useStore } from '@/store'
import { useHeaderPrimary } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

function plural(count: number): string {
  return count === 1 ? '1 comment' : `${count} comments`
}

let sends = 0

export function ReviewBar({
  run,
  agent,
  agentName,
  files,
  scope,
  repo,
}: {
  run: RunRecord
  agent: AgentTerminal
  agentName: string
  /** The diff on screen, which decides each comment's line numbers and whether it is outdated. */
  files: PatchFile[]
  scope: string
  /** A repo other than the primary: its review is kept apart, and the message says where the comments are. */
  repo?: { key: string; name: string }
}) {
  const key = repo?.key ?? run.id
  const { comments, sending, error, sent } = useReview(key)
  // The store's copy is the live one: a queued message is later sent, denied or refused.
  const delivered = useStore((s) => (sent ? s.roomMessages[run.id]?.find((entry) => entry.id === sent.message.id) : undefined))
  const agentSend = useAgentSend(run, agent)
  const [discarding, setDiscarding] = useState(false)
  const latest = useRef({ files, scope })
  latest.current = { files, scope }
  const pinned = comments.filter((comment) => comment.body).length
  useHeaderPrimary(pinned > 0)

  const deliver = async () => {
    const { files: shown, scope: at } = latest.current
    const taken = review(key).comments
    const entries = reviewEntries(taken, shown, at)
    if (review(key).sending || entries.length === 0) return
    const mark = ++sends
    patchReview(key, { sending: mark, error: undefined })
    // A review dropped meanwhile, by a sign-in or a deleted run, has a successor this must not write to.
    const settle = (patch: Partial<Review>) => {
      if (review(key).sending === mark) patchReview(key, { ...patch, sending: undefined })
    }
    try {
      const posted = await agentSend.post(reviewMessage(entries, repo?.name))
      if (posted.state !== 'sent' && posted.state !== 'queued') return settle({ error: refusal(posted) })
      const carried = new Set(entries.map((entry) => entry.comment.id))
      settle({
        // Only what the agent got goes: not a comment the message left out, nor one edited while the request was out.
        comments: review(key).comments.filter((comment) => !carried.has(comment.id) || !taken.includes(comment)),
        sent: { message: posted, count: entries.length },
      })
    } catch (cause) {
      settle({ error: message(cause) })
    }
  }

  if (pinned === 0) {
    if (!sent) return null
    const { text, tone } = receipt(delivered ?? sent.message, plural(sent.count), agentName)
    return (
      <div role="group" aria-label="Review" className="flex min-h-8 min-w-0 shrink-0 flex-wrap items-center gap-x-2 border-b border-seam bg-chrome px-3 py-0.5 text-ui-sm">
        {tone === 'done' && <Check aria-hidden className="size-3.5 shrink-0 text-state-done" />}
        <p role="status" className={cn('min-w-0 flex-1', tone === 'failed' ? 'text-state-failed' : 'text-text')}>
          {text}{' '}
          <Button variant="link" size="sm" onClick={() => useStore.getState().navigate('run', { runId: run.id, view: 'session' })}>
            Open the session
          </Button>
        </p>
        <Button variant="ghost" size="icon-sm" label="Dismiss" onClick={() => patchReview(key, { sent: undefined })}>
          <X />
        </Button>
      </div>
    )
  }

  const count = written(comments).length
  return (
    <div role="group" aria-label="Review" className="flex min-h-8 min-w-0 shrink-0 flex-wrap items-center gap-x-2 gap-y-0.5 border-b border-seam bg-chrome px-3 py-0.5 text-ui-sm">
      <span className="flex shrink-0 items-center gap-1.5 font-medium text-text tabular-nums">
        <MessageSquare aria-hidden className="size-3.5 text-muted" />
        {plural(count)}
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
            <AlertDialogTitle>Discard {plural(count)}?</AlertDialogTitle>
            <AlertDialogDescription>Nothing here has been sent to the agent, and a discarded comment cannot be brought back.</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep</AlertDialogCancel>
            <AlertDialogAction onClick={() => patchReview(key, { comments: [], error: undefined })}>Discard</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
