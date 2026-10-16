import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { api } from '@/lib/api'
import { runCommands, useCommandRunner, type Command } from '@/lib/commands'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import type { TimelinePayload } from '@/store/sessions'

/** Asks the owner to accept a reported success; Not yet hides it until the agent reports success again. */
export function CompletionPrompt({ run, className }: { run: RunRecord; className?: string }) {
  const paused = useStore((s) => s.pausedRuns[run.id])
  const predicting = useStore((s) => Boolean(s.predicting[run.id]))
  const members = useStore((s) => s.members)
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const cap = useCapability()
  const self = useSelf()
  if (run.review !== 'ready' || run.member_id !== self.id) return null
  const commands = runCommands({ run, paused, cap, members, self, steerOthers, predicting })
  const complete = commands.find((command) => command.id === 'complete')
  const later = commands.find((command) => command.id === 'not-yet')
  if (!complete || !later) return null
  return <Ask run={run} complete={complete} later={later} className={className} />
}

function Ask({ run, complete, later, className }: { run: RunRecord; complete: Command; later: Command; className?: string }) {
  const perform = useCommandRunner()
  const summary = useReportedSummary(run)
  return (
    <div className={className}>
      <Callout
        tone="review"
        role="region"
        aria-label="Completion"
        title="Your agent reports this task is finished. Mark it complete?"
        actions={
          <>
            <Button size="sm" onClick={() => void perform(complete)}>Mark complete</Button>
            <Button size="sm" variant="ghost" onClick={() => void perform(later)}>Not yet</Button>
          </>
        }
      >
        {summary && <p className="text-ui-sm text-muted">{summary}</p>}
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
