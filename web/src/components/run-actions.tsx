// The focused run's verbs as visible buttons. The list, the gates, the icons
// and the words all come from `src/lib/commands.ts`, so this bar and the
// command palette can never drift apart. The one thing this surface adds is
// the confirm step: a button is one click away from an accident, where a
// palette item is already several deliberate steps away from one.

import { UserPlus } from 'lucide-react'
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
  handoffTargets,
  runCommands,
  useCommandRunner,
  type Command,
  type RunCommandContext,
} from '@/lib/commands'
import { runLabel } from '@/lib/status'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

export function RunActions({ run }: { run: RunRecord }) {
  const paused = useStore((s) => s.pausedRuns[run.id])
  const members = useStore((s) => s.members)
  const cap = useCapability()
  const perform = useCommandRunner()
  const [asking, setAsking] = useState<Command | null>(null)
  const [handoff, setHandoff] = useState(false)

  const context: RunCommandContext = { run, paused, cap, members }
  const confirm = asking?.confirm

  return (
    <>
      {runCommands(context).map((command) => (
        <Button
          key={command.id}
          variant="ghost"
          size="sm"
          className="h-6 px-2"
          onClick={() =>
            command.confirm ? setAsking(command) : perform(command)
          }
        >
          <command.Icon className="size-3" aria-hidden />
          {buttonLabel(command.label)}
        </Button>
      ))}

      {/* Every eligible member behind one button: a viewer cannot own a run
          and the current owner is not a target, so a run with nobody to hand
          to shows nothing at all. */}
      {handoffTargets(run, members).length > 0 && (
        <Button
          variant="ghost"
          size="sm"
          className="h-6 px-2"
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
                  perform(asking)
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
                    perform(command)
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

/**
 * A trailing ellipsis promises "a form follows", which a palette item needs
 * to say and a button does not - clicking one is already the step that opens
 * it. The command keeps its palette wording; only the button trims it.
 */
function buttonLabel(label: string): string {
  return label.replace(/\.\.\.$/, '')
}
