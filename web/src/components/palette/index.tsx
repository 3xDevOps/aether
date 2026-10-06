import { useEffect, useRef, useState } from 'react'
import { PaletteBody } from '@/components/palette/palette'
import { TemplateDialog } from '@/components/palette/template-dialog'
import { RunCommandConfirmation } from '@/components/run-command-confirmation'
import { CommandDialog } from '@/components/ui/command'
import { useCommandRunner, type Command } from '@/lib/commands'
import { useKeybindings } from '@/lib/keybindings'
import { inModal } from '@/lib/keys'
import { useStore } from '@/store'
import type { RunRecord } from '@/store/runs'

export function CommandPalette() {
  const open = useStore((s) => s.paletteOpen)
  const toggle = useStore((s) => s.togglePalette)
  const identityKey = useStore((s) => s.identityKey)
  const activeWorkspace = useStore((s) => s.activeWorkspace)
  const workspaces = useStore((s) => s.workspaces)
  const context = activeWorkspace
    ? workspaces[activeWorkspace]?.name ?? 'Workspace'
    : 'All workspaces'
  const [templates, setTemplates] = useState(false)
  const [confirmation, setConfirmation] = useState<{
    identityKey: string | null
    run: RunRecord
    command: Command & { confirm: NonNullable<Command['confirm']> }
  } | null>(null)
  const pendingConfirmation = useRef<typeof confirmation>(null)
  const perform = useCommandRunner()
  const restoreFocus = useRef(true)
  const invoker = useRef<HTMLElement | null>(null)
  const invokerIdentity = useRef(identityKey)

  useEffect(() => {
    if (pendingConfirmation.current?.identityKey !== identityKey) {
      pendingConfirmation.current = null
    }
    setConfirmation((current) => current?.identityKey === identityKey ? current : null)
  }, [identityKey])

  const onShortcut = (e: KeyboardEvent) => {
    // Left to the browser, this chord opens the address bar.
    e.preventDefault()
    // Do not stack on a modal, except the palette itself, which this closes.
    // A terminal is not a modal, so it can invoke the palette.
    const s = useStore.getState()
    if (s.paletteDialog || templates || confirmation || pendingConfirmation.current) return
    if (!s.paletteOpen && inModal(e.target)) return
    // Keep the chord from becoming terminal input.
    e.stopPropagation()
    toggle()
  }
  useKeybindings('global', { palette: onShortcut, 'palette-alt': onShortcut })

  return (
    <>
      <CommandDialog
        open={open}
        showCloseButton={false}
        onOpenChange={(next: boolean) => {
          if (!next) restoreFocus.current = true
          toggle(next)
        }}
        onOpenAutoFocus={() => {
          const target = document.activeElement
          invokerIdentity.current = useStore.getState().identityKey
          invoker.current =
            target instanceof HTMLElement && target !== document.body ? target : null
        }}
        onCloseAutoFocus={(event) => {
          // Let the palette unmount before a second modal takes focus; keep the
          // invoker (even xterm) to restore on cancellation.
          if (pendingConfirmation.current) {
            event.preventDefault()
            const pending = pendingConfirmation.current
            pendingConfirmation.current = null
            if (pending.identityKey === useStore.getState().identityKey) {
              setConfirmation(pending)
            } else {
              invoker.current = null
              restoreFocus.current = true
            }
            return
          }
          const destinationOpen = useStore.getState().paletteDialog !== null || templates
          if (
            invokerIdentity.current !== useStore.getState().identityKey ||
            !restoreFocus.current ||
            destinationOpen
          ) {
            event.preventDefault()
            restoreFocus.current = true
            invoker.current = null
            return
          }

          const target = invoker.current
          restoreFocus.current = true
          invoker.current = null
          if (!target?.isConnected || target === document.body) return
          event.preventDefault()
          target.focus()
        }}
        title="Command palette"
        description={`Jump to a run or workspace, steer a run. Active workspace: ${context}.`}
      >
        <PaletteBody
          onDone={(restore = true) => {
            restoreFocus.current = restore
            toggle(false)
          }}
          onTemplates={() => setTemplates(true)}
          onConfirm={(run, command) => {
            // The item may still belong to the previous render during hydration.
            if (identityKey !== useStore.getState().identityKey) return
            pendingConfirmation.current = { identityKey, run, command }
            toggle(false)
          }}
        />
      </CommandDialog>
      {templates && <TemplateDialog onClose={() => setTemplates(false)} />}
      {confirmation && confirmation.identityKey === identityKey && (
        <RunCommandConfirmation
          run={confirmation.run}
          confirmation={confirmation.command.confirm}
          onConfirm={() => {
            const command = confirmation.command
            setConfirmation(null)
            if (confirmation.identityKey !== useStore.getState().identityKey) return
            void perform(command)
          }}
          onClose={() => setConfirmation(null)}
          onCloseAutoFocus={(event) => {
            event.preventDefault()
            const target = invoker.current
            invoker.current = null
            if (
              confirmation.identityKey === useStore.getState().identityKey &&
              target?.isConnected
            ) target.focus()
          }}
        />
      )}
    </>
  )
}
