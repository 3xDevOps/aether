import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { api } from '@/lib/api'
import { runCommands, useCommandRunner, type Command } from '@/lib/commands'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'
import { messageScopeKey, useMessageList } from '@/store/messages'
import type { RunRecord } from '@/store/runs'

/** Asks the owner to accept a reported success; Not yet hides it until the agent reports success again. */
export function CompletionPrompt({ run, className }: { run: RunRecord; className?: string }) {
  const paused = useStore((s) => s.pausedRuns[run.id])
  const members = useStore((s) => s.members)
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const cap = useCapability()
  const self = useSelf()
  if (run.review !== 'ready') return null
  const commands = runCommands({ run, paused, cap, members, self, steerOthers })
  const complete = commands.find((command) => command.id === 'complete')
  const later = commands.find((command) => command.id === 'not-yet')
  if (!complete || !later) return null
  return <Ask run={run} complete={complete} later={later} className={className} />
}

function Ask({ run, complete, later, className }: { run: RunRecord; complete: Command; later: Command; className?: string }) {
  const perform = useCommandRunner()
  const scope = { kind: 'run', workspaceID: run.workspace_id, runID: run.id } as const
  useMessageList(useStore, api, scope)
  const summary = useStore((s) => {
    const ids = s.messageLists[messageScopeKey(scope)]?.ids ?? []
    for (let i = ids.length - 1; i >= 0; i--) {
      const message = s.runMessages[ids[i]]
      if (message?.kind === 'report' && message.from_run_id === run.id && message.outcome === 'success') return message.summary || message.body
    }
    return undefined
  })
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
