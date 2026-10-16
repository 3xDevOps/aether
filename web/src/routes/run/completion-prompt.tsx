import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { runCommands, useCommandRunner } from '@/lib/commands'
import { plainReason } from '@/lib/reasons'
import { cn } from '@/lib/utils'
import { sessionColumn } from '@/routes/run/session-rows'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'
import { isTerminal, type RunRecord } from '@/store/runs'

/** Asks the owner to accept a reported success; Not yet hides it until the agent reports success again. */
export function CompletionPrompt({ run }: { run: RunRecord }) {
  const paused = useStore((s) => s.pausedRuns[run.id])
  const members = useStore((s) => s.members)
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const cap = useCapability()
  const self = useSelf()
  const perform = useCommandRunner()
  if (run.review !== 'ready') return null
  const commands = runCommands({ run, paused, cap, members, self, steerOthers })
  const complete = commands.find((command) => command.id === 'complete')
  const later = commands.find((command) => command.id === 'not-yet')
  if (!complete || !later) return null
  const summary = run.reason && run.reason !== 'agent reported success' && !isTerminal(run.status) ? plainReason(run.reason) : undefined
  return (
    <div className={cn(sessionColumn, 'pb-2')}>
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
