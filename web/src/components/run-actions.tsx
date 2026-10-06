import { useRef, useState } from 'react'
import type * as React from 'react'
import { Ellipsis, LoaderCircle, User } from '@/components/icons'
import { RunCommandConfirmation } from '@/components/run-command-confirmation'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { handoffCommands, runCommands, useCommandRunner } from '@/lib/commands'
import type { Command, RunCommandContext } from '@/lib/commands'
import { runLabel } from '@/lib/status'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

const replaced = new Set(['inject'])
const closing = new Set(['close', 'kill', 'delete'])

export interface ExtraItem {
  id: string
  label: string
  Icon: React.ComponentType<{ className?: string }>
  onSelect: (returnTo: HTMLElement | null) => void
  description?: string
  disabled?: boolean
}

export function RunActions({ run, extra = [], compact = false }: { run: RunRecord; extra?: ExtraItem[]; compact?: boolean }) {
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
  const trigger = useRef<HTMLButtonElement>(null)
  const openingDialog = useRef(false)

  const context: RunCommandContext = { run, paused, cap, members, self, steerOthers }
  const commands = runCommands(context).filter((command) => !replaced.has(command.id))
  const handoffs = handoffCommands(context)
  const everyday = commands.filter((command) => !closing.has(command.id))
  const ending = commands.filter((command) => closing.has(command.id))

  const start = (command: Command) => {
    if (inFlight.current || command.disabled) return
    inFlight.current = true
    setRunning(command.id)
    void perform(command).finally(() => {
      inFlight.current = false
      setRunning(null)
    })
  }

  const restoreFocus = (event: Event) => {
    event.preventDefault()
    trigger.current?.focus()
  }

  const item = (command: Command) => (
    <MenuItem
      key={command.id}
      tone={command.confirm ? 'danger' : undefined}
      disabled={running !== null || command.disabled}
      onSelect={() => {
        if (inFlight.current || command.disabled) return
        openingDialog.current = Boolean(command.confirm) || !command.done
        if (command.confirm) setAsking(command)
        else start(command)
      }}
    >
      <command.Icon aria-hidden />
      {command.label}
    </MenuItem>
  )

  return (
    <>
      <Menu
        open={menuOpen}
        onOpenChange={(open) => {
          if (open && inFlight.current) return
          setMenuOpen(open)
        }}
      >
        <MenuTrigger asChild>
          {compact ? (
            <Button ref={trigger} variant="ghost" size="icon" label="More" aria-disabled={running !== null || undefined}>
              {running !== null ? <LoaderCircle className="animate-spin motion-reduce:animate-none" /> : <Ellipsis />}
            </Button>
          ) : (
            <Button ref={trigger} variant="ghost" size="sm" aria-disabled={running !== null || undefined}>
              {running !== null ? <LoaderCircle className="animate-spin motion-reduce:animate-none" aria-hidden /> : <Ellipsis aria-hidden />}
              More
            </Button>
          )}
        </MenuTrigger>
        <MenuContent
          align="end"
          onCloseAutoFocus={(event) => {
            if (!openingDialog.current) return
            openingDialog.current = false
            event.preventDefault()
          }}
        >
          {everyday.map(item)}
          {handoffs.length > 0 && (
            <MenuItem
              disabled={running !== null}
              onSelect={() => {
                if (inFlight.current) return
                openingDialog.current = true
                setHandoff(true)
              }}
            >
              <User aria-hidden />
              Hand off…
            </MenuItem>
          )}
          {extra.length > 0 && (everyday.length > 0 || handoffs.length > 0) && <MenuSeparator />}
          {extra.map((entry) => (
            <MenuItem
              key={entry.id}
              disabled={entry.disabled}
              description={entry.description}
              icon={<entry.Icon aria-hidden />}
              onSelect={() => entry.onSelect(trigger.current)}
            >
              {entry.label}
            </MenuItem>
          ))}
          {ending.length > 0 && <MenuSeparator />}
          {ending.map(item)}
        </MenuContent>
      </Menu>

      {asking?.confirm && (
        <RunCommandConfirmation
          run={run}
          confirmation={asking.confirm}
          onConfirm={() => start(asking)}
          onClose={() => setAsking(null)}
          onCloseAutoFocus={restoreFocus}
        />
      )}

      {handoff && (
        <Dialog open onOpenChange={() => setHandoff(false)}>
          <DialogContent onCloseAutoFocus={restoreFocus}>
            <DialogHeader>
              <DialogTitle>Hand off this run</DialogTitle>
              <DialogDescription>
                Whoever you pick owns &quot;{runLabel(run)}&quot; from here on.
              </DialogDescription>
            </DialogHeader>
            <div className="flex flex-col items-stretch gap-1">
              {handoffs.map((command) => (
                <Button
                  key={command.id}
                  variant="secondary"
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
