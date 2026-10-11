import { useRef, useState } from 'react'
import { Check, ExternalLink, RefreshCw } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { RelativeTime } from '@/components/ui/relative-time'
import { SectionLabel } from '@/components/ui/section-label'
import { StatusDot, type Tone } from '@/components/ui/status-dot'
import { message } from '@/lib/format'
import type { RoomMessage, RunPRCheck, RunPRFeedbackResult } from '@/lib/types'
import { cn } from '@/lib/utils'
import { receipt, refusal, useAgentSend } from '@/routes/diff/agent-send'
import { Diagnostics, InlineError } from '@/routes/diff/publish-parts'
import { checkState, countChecks, refreshPullRequest, stateLabel, type CheckState, type TrackedPR } from '@/routes/diff/publish-pr'
import { implicitControlWindowMs, type AgentTerminal } from '@/routes/run/agent-terminal'
import { useStore } from '@/store'
import type { RunRecord } from '@/store/runs'

const link = 'text-accent underline-offset-2 hover:underline'

const order: CheckState[] = ['failing', 'running', 'passing']
const checkTone: Record<CheckState, Tone> = { failing: 'failed', running: 'working', passing: 'done' }
const checkWord: Record<CheckState, string> = { failing: 'Failing', running: 'Running', passing: 'Passed' }

function checkSummary(checks: RunPRCheck[]): string {
  const counts = countChecks(checks)
  return [
    counts.failing && `${counts.failing} failing`,
    counts.running && `${counts.running} running`,
    counts.passing && `${counts.passing} passed`,
  ].filter(Boolean).join(' · ')
}

interface Comment {
  key: string
  author: string
  /** The file and lines an inline comment is on. */
  lines?: string
  /** A review's verdict. */
  verdict?: string
  at: string
  body: string
  url?: string
  /** What the agent is sent in place of the row. */
  text: string
}

function commentsOf(feedback: RunPRFeedbackResult): Comment[] {
  return [
    ...(feedback.reviews ?? []).filter((review) => review.body || review.state === 'APPROVED' || review.state === 'CHANGES_REQUESTED').map((review) => ({
      key: `review:${review.id}`, author: review.author, verdict: words(review.state), at: review.submitted_at, body: review.body,
      text: `Review by ${review.author}: ${review.state} · ${review.submitted_at}\n${review.body}`,
    })),
    ...(feedback.review_comments ?? []).map((comment) => {
      const lines = `${comment.path}:${comment.start_line ? `${comment.start_line}–` : ''}${comment.line ?? comment.original_line ?? '?'}`
      return {
        key: `inline:${comment.id}`, author: comment.author, lines, at: comment.created_at, body: comment.body, url: comment.url,
        text: `Inline review by ${comment.author}: ${lines} (${comment.side ?? 'unknown side'})\n${comment.body}\nCreated: ${comment.created_at}\nCommit: ${comment.commit_oid}${comment.original_line ? `\nOriginal line: ${comment.original_line}` : ''}${comment.in_reply_to_id ? `\nReply to: ${comment.in_reply_to_id}` : ''}${comment.diff_hunk ? `\n${comment.diff_hunk}` : ''}\n${comment.url}`,
      }
    }),
    ...(feedback.comments ?? []).map((comment) => ({
      key: `comment:${comment.id}`, author: comment.author, at: comment.created_at, body: comment.body, url: comment.url,
      text: `Comment by ${comment.author} · ${comment.created_at}\n${comment.body}\n${comment.url}`,
    })),
  ].sort((a, b) => a.at.localeCompare(b.at))
}

/** GitHub's `CHANGES_REQUESTED` and `timed_out`, as words. */
function words(state: string): string {
  return state.toLowerCase().replaceAll('_', ' ')
}

function checkText(check: RunPRCheck): string {
  return `Failing check: ${check.name}\nConclusion: ${check.conclusion || check.status}${check.url ? `\n${check.url}` : ''}`
}

/** The run's pull request as GitHub reports it: its checks and its review
 * comments, each failing check and each comment one click from the agent. */
export function PRFeedback({
  run,
  agent,
  agentName,
  tracked,
  open,
  onOpenChange,
}: {
  run: RunRecord
  agent: AgentTerminal
  agentName: string
  tracked: TrackedPR
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const agentSend = useAgentSend(run, agent)
  const [sent, setSent] = useState<Record<string, RoomMessage>>({})
  const [failed, setFailed] = useState<Record<string, string>>({})
  const [sending, setSending] = useState<string | null>(null)
  const [refreshing, setRefreshing] = useState(false)
  const inFlight = useRef(false)
  const feedback = tracked.feedback
  const pr = feedback?.pull_request
  if (!feedback || !pr) return null

  const checks = [...(feedback.checks ?? [])].sort((a, b) => order.indexOf(checkState(a)) - order.indexOf(checkState(b)))
  const passing = checks.filter((check) => checkState(check) === 'passing')
  const pending = checks.filter((check) => checkState(check) !== 'passing')
  const comments = commentsOf(feedback)
  const sendable = comments.length > 0 || pending.some((check) => checkState(check) === 'failing')

  // The row is reserved before control is asked for: a second click meanwhile
  // would replace the send that is waiting for it.
  const send = (key: string, text: string) => {
    if (inFlight.current) return
    inFlight.current = true
    setSending(key)
    const settle = (error = '') => {
      inFlight.current = false
      setSending(null)
      setFailed((all) => ({ ...all, [key]: error }))
    }
    const dropped = window.setTimeout(() => settle('Control of the run did not arrive, so nothing was sent.'), implicitControlWindowMs + 500)
    const body = `GitHub feedback on ${pr.repository}#${pr.number} (${pr.url}), ${pr.head_repository}:${pr.head_branch} → ${pr.base_branch}, head ${pr.head_oid}:\n\n${text}`
    agentSend.dispatch(() => {
      clearTimeout(dropped)
      void agentSend.post(body).then(
        (posted) => {
          if (posted.state !== 'sent' && posted.state !== 'queued') return settle(refusal(posted))
          setSent((all) => ({ ...all, [key]: posted }))
          settle()
        },
        (cause) => settle(message(cause)),
      )
    })
  }
  const sender = (key: string, what: string, text: string) => (
    <Sender
      run={run}
      agentName={agentName}
      what={what}
      sent={sent[key]}
      error={failed[key]}
      sending={sending === key}
      disabled={sending !== null || agentSend.blocked !== null}
      onSend={() => send(key, text)}
      onSession={() => {
        onOpenChange(false)
        useStore.getState().navigate('run', { runId: run.id, view: 'session' })
      }}
    />
  )
  const row = (check: RunPRCheck) => {
    const state = checkState(check)
    const key = `check:${pr.head_oid}:${check.name}:${check.url ?? ''}`
    return (
      <li key={key} className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 px-2 py-1.5">
        <StatusDot tone={checkTone[state]} label={checkWord[state]} />
        <span className="min-w-0 flex-1 basis-40 break-words">
          {check.url ? <a className="text-text underline-offset-2 hover:underline" href={check.url} target="_blank" rel="noreferrer">{check.name}</a> : check.name}
          <span className="text-muted"> · {words(check.conclusion || check.status)}</span>
        </span>
        {state === 'failing' && sender(key, '1 check', checkText(check))}
      </li>
    )
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="md:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Pull request #{pr.number}</DialogTitle>
          <DialogDescription className="break-words">
            <a className={cn(link, 'inline-flex items-baseline gap-1')} href={pr.url} target="_blank" rel="noreferrer">
              {pr.title}
              <ExternalLink aria-hidden className="size-3 shrink-0 self-center" />
            </a>
            <span className="block text-ui-sm">
              {stateLabel(pr)} · <span className="font-code">{pr.head_repository}:{pr.head_branch}</span> into{' '}
              <span className="font-code">{pr.repository}:{pr.base_branch}</span>
            </span>
          </DialogDescription>
        </DialogHeader>
        <InlineError>{tracked.error}</InlineError>
        <Diagnostics output={feedback.output} error={feedback.error} />
        {feedback.truncated && (
          <p role="alert" className="text-ui-sm text-state-needs-you">GitHub returned more than is shown here. Open the pull request for the rest.</p>
        )}
        {sendable && (agentSend.blocked ?? agentSend.waits) && (
          <p className="text-ui-sm text-muted">
            {agentSend.blocked ?? 'What you send from here reaches the agent in 45 s unless the run’s controller decides sooner.'}
          </p>
        )}
        <section aria-label="Checks" className="grid min-w-0 gap-1.5 text-ui-sm">
          <div className="flex min-h-6 items-center gap-2">
            <SectionLabel as="h3">Checks</SectionLabel>
            <span className="min-w-0 flex-1 truncate text-muted tabular-nums">{checkSummary(checks)}</span>
            <Button
              variant="ghost"
              size="icon-sm"
              label="Refresh"
              disabled={refreshing}
              onClick={() => {
                setRefreshing(true)
                void refreshPullRequest(run).finally(() => setRefreshing(false))
              }}
            >
              <RefreshCw className={cn(refreshing && 'animate-spin motion-reduce:animate-none')} />
            </Button>
          </div>
          {checks.length === 0 && <p className="text-muted">No check has reported on <span className="font-code">{pr.head_oid.slice(0, 8)}</span> yet.</p>}
          {checks.length > 0 && <ul className="divide-y divide-seam rounded-panel border border-seam">{(pending.length ? pending : passing).map(row)}</ul>}
          {pending.length > 0 && passing.length > 0 && (
            <Collapsible>
              <CollapsibleTrigger>
                <span className="text-ui-sm text-muted">{passing.length} passed</span>
              </CollapsibleTrigger>
              <CollapsibleContent>
                <ul className="mt-1 divide-y divide-seam rounded-panel border border-seam">{passing.map(row)}</ul>
              </CollapsibleContent>
            </Collapsible>
          )}
        </section>
        <section aria-label="Review comments" className="grid min-w-0 gap-1.5 text-ui-sm">
          <SectionLabel as="h3">Review comments</SectionLabel>
          {comments.length === 0 ? (
            <p className="text-muted">No review comments.</p>
          ) : (
            <ul className="divide-y divide-seam rounded-panel border border-seam">
              {comments.map((comment) => (
                <li key={comment.key}>
                  <article className="grid min-w-0 gap-1 px-2 py-1.5">
                    <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
                      <span className="min-w-0 flex-1 basis-40 break-words">
                        <span className="font-medium text-text">{comment.author}</span>
                        {comment.verdict && <span className="text-muted"> {comment.verdict}</span>}
                        {comment.lines && <span className="font-code text-muted"> {comment.lines}</span>}
                        <span className="text-muted"> · <RelativeTime at={comment.at} /></span>
                      </span>
                      {sender(comment.key, '1 comment', comment.text)}
                    </div>
                    {comment.body && <p className="max-h-48 overflow-auto break-words whitespace-pre-wrap text-text">{comment.body}</p>}
                    {comment.url && (
                      <a className={cn(link, 'justify-self-start')} href={comment.url} target="_blank" rel="noreferrer">
                        Open on GitHub
                      </a>
                    )}
                  </article>
                </li>
              ))}
            </ul>
          )}
        </section>
      </DialogContent>
    </Dialog>
  )
}

/** One row's way to the agent: the button, then what became of the message. */
function Sender({
  run,
  agentName,
  what,
  sent,
  error,
  sending,
  disabled,
  onSend,
  onSession,
}: {
  run: RunRecord
  agentName: string
  what: string
  sent: RoomMessage | undefined
  error: string | undefined
  sending: boolean
  disabled: boolean
  onSend: () => void
  onSession: () => void
}) {
  // The store's copy is the live one: a queued message is later sent, denied or refused.
  const delivered = useStore((s) => (sent ? s.roomMessages[run.id]?.find((entry) => entry.id === sent.id) : undefined))
  if (sent) {
    const { text, tone } = receipt(delivered ?? sent, what, agentName)
    return (
      <p role="status" className={cn('flex min-w-0 basis-full items-baseline gap-1', tone === 'failed' ? 'text-state-failed' : 'text-text')}>
        {tone === 'done' && <Check aria-hidden className="size-3.5 shrink-0 self-center text-state-done" />}
        <span className="min-w-0">
          {text}{' '}
          <Button variant="link" size="sm" onClick={onSession}>
            Open the session
          </Button>
        </span>
      </p>
    )
  }
  return (
    <>
      <Button variant="secondary" size="sm" disabled={disabled} onClick={onSend}>
        {sending ? 'Sending…' : 'Send to agent'}
      </Button>
      {error && <p role="alert" className="min-w-0 basis-full break-words text-state-failed">{error}</p>}
    </>
  )
}
