import { Ellipsis, Loader2, UserPlus } from 'lucide-react'
import { useRef, useState } from 'react'
import { RunCommandConfirmation } from '@/components/run-command-confirmation'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Menu,
  MenuContent,
  MenuItem,
  MenuSeparator,
  MenuTrigger,
} from '@/components/ui/menu'
import {
  handoffCommands,
  runCommands,
  useCommandRunner,
} from '@/lib/commands'
import type { Command, RunCommandContext } from '@/lib/commands'
import { runLabel } from '@/lib/status'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'
import { isTerminal, type RunRecord } from '@/store/runs'

export function RunActions({ run }: { run: RunRecord }) {
  const paused = useStore((s) => s.pausedRuns[run.id])
  const members = useStore((s) => s.members)
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const cap = useCapability()
  const self = useSelf()
  const perform = useCommandRunner()
  const [asking, setAsking] = useState<Command | null>(null)
  const [handoff, setHandoff] = useState(false)
  // The ref locks synchronously, including two activations before React paints.
  const inFlight = useRef(false)
  const [running, setRunning] = useState<string | null>(null)
  const [menuOpen, setMenuOpen] = useState(false)
  const moreTrigger = useRef<HTMLButtonElement>(null)
  const openingDialog = useRef(false)

  const context: RunCommandContext = { run, paused, cap, members, self, steerOthers }
  const commands = runCommands(context)
  const handoffs = handoffCommands(context)
  const primaryIds = run.archived_at
    ? ['restore', 'relaunch', 'release']
    : run.status === 'completed'
      ? ['close', 'relaunch', 'release']
      : isTerminal(run.status)
        ? ['relaunch', 'release', 'archive']
        : ['inject', paused ? 'resume' : 'pause']
  const primary = primaryIds.flatMap((id) => commands.filter((command) => command.id === id))
  const overflow = commands.filter((command) => !primaryIds.includes(command.id))
  const secondary = overflow.filter((command) => !command.confirm)
  const destructive = overflow.filter((command) => command.confirm)

  const start = (command: Command) => {
    if (inFlight.current || command.disabled) return
    inFlight.current = true
    setRunning(command.id)
    void perform(command).finally(() => {
      inFlight.current = false
      setRunning(null)
    })
  }

  const select = (command: Command) => {
    if (inFlight.current || command.disabled) return
    if (command.confirm) setAsking(command)
    else start(command)
  }

  const restoreMoreFocus = (event: Event) => {
    event.preventDefault()
    moreTrigger.current?.focus()
  }

  const menuItem = (command: Command) => (
    <MenuItem
      key={command.id}
      disabled={running !== null || command.disabled}
      onSelect={() => {
        if (inFlight.current || command.disabled) return
        openingDialog.current = Boolean(command.confirm) || !command.done
        select(command)
      }}
    >
      <command.Icon className="size-3" aria-hidden />
      {command.label}
    </MenuItem>
  )

  return (
    <>
      {primary.map((command) => {
        const blocked = running !== null || command.disabled === true
        const buttonLabel = command.short ?? command.label
        return (
          <Button
            key={command.id}
            hint={command.label}
            variant="secondary"
            size="sm"
            aria-disabled={blocked || undefined}
            onClick={() => select(command)}
          >
            {running === command.id ? (
              <Loader2 className="size-3 animate-spin" aria-hidden />
            ) : (
              <command.Icon className="size-3" aria-hidden />
            )}
            {buttonLabel}
          </Button>
        )
      })}

      <Menu
        open={menuOpen}
        onOpenChange={(open) => {
          if (open && inFlight.current) return
          setMenuOpen(open)
        }}
      >
        <MenuTrigger asChild>
          <Button
            ref={moreTrigger}
            variant="ghost"
            size="sm"
            aria-disabled={running !== null || undefined}
          >
            {running !== null && !primaryIds.includes(running) ? (
              <Loader2 className="size-3 animate-spin" aria-hidden />
            ) : (
              <Ellipsis className="size-3" aria-hidden />
            )}
            More
          </Button>
        </MenuTrigger>
        <MenuContent
          align="end"
          onCloseAutoFocus={(event) => {
            if (!openingDialog.current) return
            openingDialog.current = false
            event.preventDefault()
          }}
        >
          {secondary.map(menuItem)}
          {handoffs.length > 0 && (
            <MenuItem
              disabled={running !== null}
              onSelect={() => {
                if (inFlight.current) return
                openingDialog.current = true
                setHandoff(true)
              }}
            >
              <UserPlus className="size-3" aria-hidden />
              Hand off
            </MenuItem>
          )}
          {destructive.length > 0 && (secondary.length > 0 || handoffs.length > 0) && (
            <MenuSeparator />
          )}
          {destructive.map(menuItem)}
          {overflow.length === 0 && handoffs.length === 0 && (
            <MenuItem disabled>No additional actions</MenuItem>
          )}
        </MenuContent>
      </Menu>

      {asking?.confirm && (
        <RunCommandConfirmation
          run={run}
          confirmation={asking.confirm}
          onConfirm={() => start(asking)}
          onClose={() => setAsking(null)}
          onCloseAutoFocus={restoreMoreFocus}
        />
      )}

      {handoff && (
        <Dialog open onOpenChange={() => setHandoff(false)}>
          <DialogContent
            className="max-w-[min(420px,calc(100%-2rem))] p-3 sm:p-4"
            onCloseAutoFocus={restoreMoreFocus}
          >
            <DialogHeader>
              <DialogTitle>Hand off this run</DialogTitle>
              <DialogDescription>
                Whoever you pick owns &quot;{runLabel(run)}&quot; from here on.
              </DialogDescription>
            </DialogHeader>
            <div className="flex flex-col gap-1">
              {handoffs.map((command) => (
                <Button
                  key={command.id}
                  variant="secondary"
                  size="sm"
                  className="justify-start"
                  aria-disabled={running !== null || undefined}
                  onClick={() => {
                    if (inFlight.current) return
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
