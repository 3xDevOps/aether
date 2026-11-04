import { useEffect, useId, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { api } from '@/lib/api'
import { runCommands, useCommandRunner, type Command } from '@/lib/commands'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import type { TimelinePayload } from '@/store/sessions'

/** Asks the owner to accept a reported success; Review later folds it to one line, Not yet withdraws it until the agent reports success again. */
export function CompletionPrompt({ run, className }: { run: RunRecord; className?: string }) {
  const paused = useStore((s) => s.pausedRuns[run.id])
  const predicting = useStore((s) => Boolean(s.predicting[run.id]))
  const members = useStore((s) => s.members)
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const cap = useCapability()
  const self = useSelf()
  if ((run.review !== 'ready' && run.review !== 'later') || run.member_id !== self.id) return null
  // The prompt shows with the session, so what the agent left is open.
  const commands = runCommands({ run: { ...run, finish_unopened: false }, paused, cap, members, self, steerOthers, predicting })
  const complete = commands.find((command) => command.id === 'complete')
  const notYet = commands.find((command) => command.id === 'not-yet')
  if (!complete) return null
  const later = commands.find((command) => command.id === 'review-later')
  return <Ask run={run} complete={complete} notYet={notYet} later={later} className={className} />
}

function Ask({ run, complete, notYet, later, className }: {
  run: RunRecord
  complete: Command
  notYet?: Command
  later?: Command
  className?: string
}) {
  const perform = useCommandRunner()
  const summary = useReportedSummary(run)
  const [reviewing, setReviewing] = useState(false)
  const [expanded, setExpanded] = useState(false)
  const closes = useId()
  if (run.review === 'later' && !reviewing) {
    return (
      <div className={className}>
        <div role="region" aria-label="Completion" className="flex items-center gap-1 text-ui-sm text-muted">
          <span>Finished, kept for later ·</span>
          <Button size="sm" variant="link" onClick={() => setReviewing(true)}>Review now</Button>
        </div>
      </div>
    )
  }
  const long = summary !== undefined && (summary.split('\n').length > 3 || summary.length > 280)
  return (
    <div className={className}>
      <Callout
        tone="review"
        role="region"
        aria-label="Completion"
        title={<span className="text-text">Your agent reports this task is finished. <span className="whitespace-nowrap">Mark complete?</span></span>}
        actions={
          <>
            <Button size="sm" variant="secondary" aria-describedby={closes} onClick={() => void perform(complete)}>Mark complete</Button>
            {notYet && <Button size="sm" variant="secondary" onClick={() => void perform(notYet)}>Not yet</Button>}
            <Button size="sm" variant="ghost" onClick={() => (later ? void perform(later) : setReviewing(false))}>Review later</Button>
            <span id={closes} className="text-ui-sm text-muted">Closes the session.</span>
          </>
        }
      >
        {summary && (
          <>
            <p className={cn('text-ui-sm whitespace-pre-wrap text-muted', !expanded && 'line-clamp-3')}>{summary}</p>
            {long && <Button variant="link" size="sm" onClick={() => setExpanded((open) => !open)}>{expanded ? 'Show less' : 'Show more'}</Button>}
          </>
        )}
      </Callout>
    </div>
  )
}

/** The summary of the run's latest success report, which the server records on the workspace timeline. */
function useReportedSummary(run: RunRecord): string | undefined {
  const [summary, setSummary] = useState<string>()
  useEffect(() => {
    let live = true
    api
      .workspaceTimeline({ workspace_id: run.workspace_id, run_id: run.id, types: ['workspace.timeline'], newest: true })
      .then((page) => {
        const reports = page.events.map((e) => (e.payload ?? {}) as TimelinePayload).filter((p) => p.kind === 'report' && p.outcome === 'success')
        if (live) setSummary(reports.at(-1)?.summary)
      })
      .catch(() => {})
    return () => {
      live = false
    }
  }, [run.workspace_id, run.id, run.review_changed_at])
  return summary
}
