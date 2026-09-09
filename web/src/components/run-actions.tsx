// The header row cannot scroll, so a row too narrow for every verb has to
// hide buttons rather than push them past the right edge.

import { Ellipsis, Loader2, UserPlus } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
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
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  handoffCommands,
  runCommands,
  useCommandRunner,
  type Command,
  type RunCommandContext,
} from '@/lib/commands'
import { runLabel } from '@/lib/status'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

/** The verbs that stay on the row at every width: whatever moves the run on. */
const primaryCommands = new Set(['pause', 'resume', 'inject', 'close', 'kill', 'relaunch'])

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
  const [menuOpen, setMenuOpen] = useState(false)
  const moreTrigger = useRef<HTMLButtonElement>(null)
  const widened = useRef(false)

  // CSS cannot close what it hides. A menu still open when the row widens
  // past the threshold is left floating in the page's top left corner, over a
  // body the modal menu has made inert, and the click that dismisses it never
  // reaches whatever it lands on. A trigger the container query has hidden
  // measures 0x0, which is what this watches for.
  useEffect(() => {
    const trigger = moreTrigger.current
    if (!menuOpen || !trigger) return
    const observer = new ResizeObserver(([entry]) => {
      if (entry.contentRect.width !== 0) return
      widened.current = true
      setMenuOpen(false)
    })
    observer.observe(trigger)
    return () => observer.disconnect()
  }, [menuOpen])

  const context: RunCommandContext = { run, paused, cap, members, self, steerOthers }
  const confirm = asking?.confirm
  const commands = runCommands(context)
  const handoffs = handoffCommands(context)
  const overflow = commands.filter((command) => !primaryCommands.has(command.id))

  const start = (command: Command) => {
    setRunning(command.id)
    void perform(command).finally(() => setRunning(null))
  }

  return (
    <>
      {commands.map((command) => (
        <Button
          key={command.id}
          variant="ghost"
          size="sm"
          className={cn(
            'h-6 px-2',
            !primaryCommands.has(command.id) && 'hidden @4xl/header:inline-flex',
          )}
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
      {handoffs.length > 0 && (
        <Button
          variant="ghost"
          size="sm"
          className="hidden h-6 px-2 @4xl/header:inline-flex"
          title="Hand off to another member"
          disabled={running !== null}
          onClick={() => setHandoff(true)}
        >
          <UserPlus className="size-3" aria-hidden />
          Hand off
        </Button>
      )}

      {(overflow.length > 0 || handoffs.length > 0) && (
        <DropdownMenu open={menuOpen} onOpenChange={setMenuOpen}>
          <DropdownMenuTrigger asChild>
            <Button
              ref={moreTrigger}
              variant="ghost"
              size="sm"
              className="h-6 px-2 @4xl/header:hidden"
              title="More actions"
              disabled={running !== null}
            >
              {running !== null ? (
                <Loader2 className="size-3 animate-spin" aria-hidden />
              ) : (
                <Ellipsis className="size-3" aria-hidden />
              )}
              More
            </Button>
          </DropdownMenuTrigger>
          {/* Radix hands focus back to the trigger, which the container query
              has just hidden, and focusing a hidden element drops focus to
              the body. Only the forced close needs the substitute. */}
          <DropdownMenuContent
            align="end"
            onCloseAutoFocus={(event) => {
              if (!widened.current) return
              widened.current = false
              event.preventDefault()
              const row = moreTrigger.current?.parentElement
              const first = Array.from(row?.querySelectorAll('button') ?? []).find(
                (button) => button !== moreTrigger.current,
              )
              first?.focus()
            }}
          >
            {overflow.map((command) => (
              <DropdownMenuItem
                key={command.id}
                title={command.label}
                disabled={command.disabled}
                onSelect={() =>
                  command.confirm ? setAsking(command) : start(command)
                }
              >
                <command.Icon className="size-3" aria-hidden />
                {command.short ?? command.label}
              </DropdownMenuItem>
            ))}
            {handoffs.length > 0 && (
              <DropdownMenuItem onSelect={() => setHandoff(true)}>
                <UserPlus className="size-3" aria-hidden />
                Hand off
              </DropdownMenuItem>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
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
              {handoffs.map((command) => (
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
