import { useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { Run, RunPRFeedbackResult } from '@/lib/types'
import { Actual, Attest, Diagnostics, InlineError } from '@/routes/diff/publish-parts'
import { useStore } from '@/store'

interface FeedbackEntry {
  key: string
  heading: string
  body: string
  url?: string
}

function entriesOf(feedback: RunPRFeedbackResult): FeedbackEntry[] {
  return [
    ...(feedback.checks ?? []).map((check, index) => ({ key: `check:${index}`, heading: `Check: ${check.name}`, body: `${check.status}${check.conclusion ? ` · ${check.conclusion}` : ''}`, url: check.url })),
    ...(feedback.comments ?? []).map((comment) => ({ key: `comment:${comment.id}`, heading: `Comment by ${comment.author} · ${comment.created_at}`, body: comment.body, url: comment.url })),
    ...(feedback.reviews ?? []).map((review) => ({ key: `review:${review.id}`, heading: `Review by ${review.author}: ${review.state} · ${review.submitted_at}`, body: review.body })),
    ...(feedback.review_comments ?? []).map((comment) => ({
      key: `inline:${comment.id}`,
      heading: `Inline review by ${comment.author}: ${comment.path}:${comment.start_line ? `${comment.start_line}–` : ''}${comment.line ?? comment.original_line ?? '?'} (${comment.side ?? 'unknown side'})`,
      body: `${comment.body}\nCreated: ${comment.created_at}\nCommit: ${comment.commit_oid}${comment.original_line ? `\nOriginal line: ${comment.original_line}` : ''}${comment.start_side ? `\nStart side: ${comment.start_side}` : ''}${comment.in_reply_to_id ? `\nReply to: ${comment.in_reply_to_id}` : ''}${comment.diff_hunk ? `\n${comment.diff_hunk}` : ''}`,
      url: comment.url,
    })),
  ]
}

export function PRFeedback({ run, feedback, client }: { run: Run; feedback: RunPRFeedbackResult; client: Api }) {
  const [selected, setSelected] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [receipt, setReceipt] = useState<string | null>(null)
  const pending = useRef<{ body: string; key: string } | null>(null)
  const entries = entriesOf(feedback)
  const chosen = entries.filter((entry) => selected.includes(entry.key))

  async function send() {
    if (busy || !chosen.length) return
    const pr = feedback.pull_request
    const body = `Selected GitHub feedback${pr ? ` for ${pr.repository}#${pr.number} (${pr.url}), ${pr.head_repository}:${pr.head_branch} → ${pr.base_branch}, head ${pr.head_oid}` : ''}:\n\n${chosen.map((entry) => `${entry.heading}\n${entry.body}${entry.url ? `\n${entry.url}` : ''}`).join('\n\n')}`
    // The same selection resends under the same key, so a lost response cannot post twice.
    const action = pending.current && pending.current.body === body ? pending.current : { body, key: crypto.randomUUID() }
    pending.current = action
    setBusy(true)
    setError(null)
    try {
      const result = await client.runRoomPost({ workspace_id: run.workspace_id, run_id: run.id, kind: 'steer_request', body, idempotency_key: action.key })
      useStore.getState().upsertRoomMessage(result.message)
      setReceipt(result.receipt ?? result.message.state)
      setSelected([])
      pending.current = null
    } catch (cause) {
      setError(message(cause))
    } finally {
      setBusy(false)
    }
  }

  return (
    <section aria-label="PR feedback" className="grid min-w-0 gap-2 text-ui-sm">
      <p className="text-muted">
        Feedback identity: {feedback.identity || 'Unavailable'} · Agent account: {feedback.account_member_id}
      </p>
      <Actual actual={feedback.actual} />
      <Diagnostics output={feedback.output} error={feedback.error} />
      {feedback.truncated && (
        <p role="alert" className="text-state-needs-you">
          Feedback is truncated; open GitHub for the complete discussion.
        </p>
      )}
      {entries.length > 0 && (
        <ul className="divide-y divide-seam rounded-panel border border-seam">
          {entries.map((entry) => (
            <li key={entry.key}>
              <article className="grid min-w-0 gap-1 p-2">
                <Attest
                  checked={selected.includes(entry.key)}
                  disabled={busy}
                  onChange={() => setSelected((current) => current.includes(entry.key) ? current.filter((key) => key !== entry.key) : [...current, entry.key])}
                >
                  <span className="font-medium break-words">{entry.heading}</span>
                </Attest>
                <p className="max-h-64 overflow-auto pl-6 break-words whitespace-pre-wrap text-muted">{entry.body}</p>
                {entry.url && (
                  <a className="pl-6 text-accent underline-offset-2 hover:underline" href={entry.url} target="_blank" rel="noreferrer">
                    Open on GitHub
                  </a>
                )}
              </article>
            </li>
          ))}
        </ul>
      )}
      <p className="text-muted">
        Only checked feedback is sent. It goes to the agent as a message, with the same permission, approval and delivery rules as
        the Session composer; this does not type directly into the agent.
      </p>
      <div className="grid justify-items-start gap-1">
        <Button disabled={busy || !chosen.length} onClick={() => void send()}>
          Send selected feedback to the agent
        </Button>
        <InlineError>{error ?? undefined}</InlineError>
      </div>
      {receipt && (
        <p role="status">
          Delivery: {receipt}.{' '}
          <Button variant="link" size="sm" onClick={() => useStore.getState().navigate('run', { runId: run.id, view: 'session' })}>
            Open the session
          </Button>
        </p>
      )}
    </section>
  )
}
