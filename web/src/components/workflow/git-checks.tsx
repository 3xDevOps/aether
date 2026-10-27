import { AskAssistantButton } from '@/components/assistant/ask'
import { Sparkles } from '@/components/icons'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { RelativeTime } from '@/components/ui/relative-time'
import { StatusDot, type Tone } from '@/components/ui/status-dot'
import { Tooltip } from '@/components/ui/tooltip'
import type { RunPRFeedbackResult } from '@/lib/types'
import { cn, focusRing } from '@/lib/utils'
import { commentsOf } from '@/routes/diff/publish-feedback'
import { checkPills, reviewVerdicts, type CheckPill, type PillState, type Readiness } from '@/store/git-flow'

const pillTone: Record<PillState, Tone> = { failing: 'failed', running: 'working', passing: 'done' }
const pill = 'inline-flex h-6 max-w-full min-w-0 items-center gap-1.5 rounded-full border border-seam px-2 text-ui-sm'

/** The run and pull request a failed check is asked about. */
export interface CheckTarget {
  runId: string
  workspaceId: string
}

function Pill({ entry, feedback, target }: { entry: CheckPill; feedback: RunPRFeedbackResult; target?: CheckTarget }) {
  const { check } = entry
  const body = (
    <>
      <StatusDot tone={pillTone[entry.state]} pulse={entry.state === 'running'} />
      <span className="min-w-0 truncate text-text">{entry.name}</span>
      <span className="shrink-0 text-muted">{entry.word}</span>
    </>
  )
  const tip = (
    <span className="grid gap-0.5">
      <span className="font-medium">{check.name}</span>
      <span>{[check.status.toLowerCase(), check.conclusion?.toLowerCase()].filter(Boolean).join(' · ')}</span>
      {check.url && <span className="text-muted">Opens the check on GitHub</span>}
    </span>
  )
  const pr = feedback.pull_request
  return (
    <li className="flex min-w-0 items-center gap-0.5">
      <Tooltip content={tip}>
        {check.url ? (
          <a className={cn(focusRing, pill, 'hover:bg-hover-chrome')} href={check.url} target="_blank" rel="noreferrer">{body}</a>
        ) : (
          <span tabIndex={0} className={cn(focusRing, pill)}>{body}</span>
        )}
      </Tooltip>
      {target && entry.state === 'failing' && (
        <AskAssistantButton
          variant="ghost"
          size="icon-sm"
          label={`Fix ${check.name} with the assistant`}
          hint="Fix this failed check"
          ask={{
            draft: 'Fix this failed check',
            attachments: [{ kind: 'check', workspace_id: target.workspaceId, run_id: target.runId, pr: pr?.number, repository: pr?.repository, check: check.name, label: check.name }],
          }}
        >
          <Sparkles />
        </AskAssistantButton>
      )}
    </li>
  )
}

export function CheckPills({ feedback, target }: { feedback: RunPRFeedbackResult; target?: CheckTarget }) {
  const pills = checkPills(feedback.checks ?? [])
  if (!pills.length) return null
  return (
    <ul aria-label="Checks" className="flex min-w-0 flex-wrap gap-1.5">
      {pills.map((entry) => <Pill key={entry.key} entry={entry} feedback={feedback} target={target} />)}
    </ul>
  )
}

export function Reviews({ feedback }: { feedback: RunPRFeedbackResult }) {
  const { approved, changes } = reviewVerdicts(feedback.reviews ?? [])
  const comments = commentsOf(feedback)
  if (!approved.length && !changes.length && !comments.length) return null
  return (
    <section aria-label="Reviews" className="grid min-w-0 gap-1 text-ui-sm">
      {changes.length > 0 && (
        <p className="flex min-w-0 items-center gap-1.5 text-text">
          <StatusDot tone="failed" />
          <span className="min-w-0 break-words">Changes requested by {changes.join(', ')}</span>
        </p>
      )}
      {approved.length > 0 && (
        <p className="flex min-w-0 items-center gap-1.5 text-text">
          <StatusDot tone="done" />
          <span className="min-w-0 break-words">Approved by {approved.join(', ')}</span>
        </p>
      )}
      {comments.length > 0 && (
        <Collapsible>
          <CollapsibleTrigger className="text-ui-sm text-muted">
            {comments.length} review {comments.length === 1 ? 'comment' : 'comments'}
          </CollapsibleTrigger>
          <CollapsibleContent>
            <ul className="mt-1 grid min-w-0 gap-2 border-l border-seam pl-3">
              {comments.map((comment) => (
                <li key={comment.key} className="grid min-w-0 gap-0.5">
                  <span className="min-w-0 break-words text-muted">
                    <span className="font-medium text-text">{comment.author}</span>
                    {comment.verdict && <> {comment.verdict}</>}
                    {comment.lines && <span className="font-code"> {comment.lines}</span>}
                    {' · '}
                    <RelativeTime at={comment.at} />
                  </span>
                  {comment.body && <p className="line-clamp-4 break-words whitespace-pre-wrap text-text">{comment.body}</p>}
                  {comment.url && (
                    <a className="justify-self-start text-accent underline-offset-2 hover:underline" href={comment.url} target="_blank" rel="noreferrer">
                      Open on GitHub
                    </a>
                  )}
                </li>
              ))}
            </ul>
          </CollapsibleContent>
        </Collapsible>
      )}
    </section>
  )
}

const stateWord = { ok: 'Done', pending: 'Pending', blocked: 'Blocked', unknown: 'Unknown' } as const

export function ReadinessList({ ready }: { ready: Readiness }) {
  return (
    <section aria-label="Merge readiness" className="grid min-w-0 gap-1 text-ui-sm">
      <h3 className={cn('font-medium', ready.ready ? 'text-state-done' : 'text-text')}>{ready.ready ? 'Ready to merge' : 'Merge readiness'}</h3>
      <ul className="grid min-w-0 gap-0.5">
        {ready.items.map((entry) => (
          <li key={entry.key} className="grid min-w-0 grid-cols-[auto_5rem_minmax(0,1fr)] items-center gap-x-1.5">
            <StatusDot tone={entry.tone} label={stateWord[entry.state]} pulse={entry.tone === 'working'} />
            <span className="text-muted">{entry.label}</span>
            <span className={cn('min-w-0 break-words', entry.state === 'unknown' ? 'text-muted' : 'text-text')}>{entry.text}</span>
          </li>
        ))}
      </ul>
    </section>
  )
}
