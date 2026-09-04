// The focused run's verbs as visible buttons. The list, the gates, the icons
// and the words all come from `src/lib/commands.ts`, so this bar and the
// command palette can never drift apart; buttons take the short label and
// keep the full one as their tooltip, because the action bar stays compact.
// The two things this surface adds are the confirm step - a button is one
// click away from an accident, where a palette item is already several
// deliberate steps away from one - and the in-flight state, which stops a
// slow verb being fired twice.

import { Loader2, UserPlus } from 'lucide-react'
import { useState } from 'react'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  handoffCommands,
  runCommands,
  useCommandRunner,
  type Command,
  type RunCommandContext,
} from '@/lib/commands'
import { runLabel } from '@/lib/status'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

export function RunActions({ run }: { run: RunRecord }) {
  const paused = useStore((s) => s.pausedRuns[run.id])
  const members = useStore((s) => s.members)
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const cap = useCapability()
  const self = useSelf()
  const perform = useCommandRunner()
  const [asking, setAsking] = useState<Command | null>(null)
  const [handoff, setHandoff] = useState(false)
  // One verb at a time per run. Pull shells out to `git fetch` and takes
  // seconds; without this a member with no feedback clicks it again and the
  // second fetch loses the ref-lock race, reporting a failure for a pull that
  // worked.
  const [running, setRunning] = useState<string | null>(null)

  const context: RunCommandContext = { run, paused, cap, members, self, steerOthers }
  const confirm = asking?.confirm

  const start = (command: Command) => {
    setRunning(command.id)
    void perform(command).finally(() => setRunning(null))
  }

  return (
    <>
      {runCommands(context).map((command) => (
        <Button
          key={command.id}
          variant="ghost"
          size="sm"
          className="h-6 px-2"
          title={command.label}
          disabled={running !== null || command.disabled}
          onClick={() => (command.confirm ? setAsking(command) : start(command))}
        >
          {running === command.id ? (
            <Loader2 className="size-3 animate-spin" aria-hidden />
          ) : (
            <command.Icon className="size-3" aria-hidden />
          )}
          {command.short ?? command.label}
        </Button>
      ))}

      {/* Every eligible member behind one button: a viewer cannot own a run
          and the current owner is not a target, so a run with nobody to hand
          to shows nothing at all. */}
      {handoffCommands(context).length > 0 && (
        <Button
          variant="ghost"
          size="sm"
          className="h-6 px-2"
          title="Hand off to another member"
          disabled={running !== null}
          onClick={() => setHandoff(true)}
        >
          <UserPlus className="size-3" aria-hidden />
          Hand off
        </Button>
      )}

      {asking && confirm && (
        <Dialog open onOpenChange={() => setAsking(null)}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>{confirm.title}</DialogTitle>
              <DialogDescription>
                "{runLabel(run)}" - {confirm.body}
              </DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button variant="outline" onClick={() => setAsking(null)}>
                Cancel
              </Button>
              <Button
                variant="destructive"
                onClick={() => {
                  setAsking(null)
                  start(asking)
                }}
              >
                {confirm.action}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}

      {handoff && (
        <Dialog open onOpenChange={() => setHandoff(false)}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Hand off this run</DialogTitle>
              <DialogDescription>
                Whoever you pick owns "{runLabel(run)}" from here on.
              </DialogDescription>
            </DialogHeader>
            <div className="flex flex-col gap-2">
              {handoffCommands(context).map((command) => (
                <Button
                  key={command.id}
                  variant="outline"
                  className="justify-start"
                  onClick={() => {
                    setHandoff(false)
                    start(command)
                  }}
                >
                  <command.Icon aria-hidden />
                  {command.label}
                </Button>
              ))}
            </div>
          </DialogContent>
        </Dialog>
      )}
    </>
  )
}
