// The command center is mounted once by AppShell. Its trigger is intentionally
// separate so the title bar can stay the single visible command entry point
// without creating a second dialog host. Launch, inject, forward and close
// forms remain in `dialogs.tsx`, hosted beside this component.

import { SearchIcon } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { PaletteBody } from '@/components/palette/palette'
import { TemplateDialog } from '@/components/palette/template-dialog'
import { CommandDialog } from '@/components/ui/command'
import { Tooltip } from '@/components/ui/heroui'
import { inModal } from '@/lib/keys'
import { shortcutLabel } from '@/lib/platform'
import { cn, focusRing } from '@/lib/utils'
import { useStore } from '@/store'

const shortcut = 'k'
const alternateShortcut = 'p'

/** The compact no-drag title-bar entry point for the command center. */
export function CommandPaletteTrigger({ disabled = false }: { disabled?: boolean } = {}) {
  const toggle = useStore((s) => s.togglePalette)
  const activeWorkspace = useStore((s) => s.activeWorkspace)
  const workspaces = useStore((s) => s.workspaces)
  const current = activeWorkspace ? workspaces[activeWorkspace]?.name : undefined
  const context = current ?? (activeWorkspace ? 'Workspace' : 'All workspaces')

  return (
    <Tooltip>
      <Tooltip.Trigger<'button'>
        render={(triggerProps) => (
          <button
            {...triggerProps}
            type="button"
            disabled={disabled}
            onClick={() => toggle(true)}
            aria-label="Commands"
            className={cn(
              focusRing,
              'flex h-[26px] min-w-0 w-full max-w-[600px] items-center justify-start gap-2 rounded-sm border border-border/70 bg-background/50 px-2 text-[12px] text-muted-foreground transition-colors hover:border-border hover:bg-toolbar-hover hover:text-foreground disabled:cursor-not-allowed disabled:opacity-60',
            )}
          >
            <SearchIcon aria-hidden className="size-3.5 shrink-0" />
            <span className="min-w-0 flex-1 truncate text-left">{context}</span>
            <span className="hidden shrink-0 font-mono text-[11px] sm:inline">
              {shortcutLabel('Shift+P')}
            </span>
          </button>
        )}
      />
      <Tooltip.Content>Command palette</Tooltip.Content>
    </Tooltip>
  )
}

export function CommandPalette() {
  const open = useStore((s) => s.paletteOpen)
  const toggle = useStore((s) => s.togglePalette)
  const activeWorkspace = useStore((s) => s.activeWorkspace)
  const workspaces = useStore((s) => s.workspaces)
  const context = activeWorkspace
    ? workspaces[activeWorkspace]?.name ?? 'Workspace'
    : 'All workspaces'
  // The template form is not one of the store's palette dialogs; its open
  // state lives here with the other dialog hosts.
  const [templates, setTemplates] = useState(false)
  const restoreFocus = useRef(true)
  const invoker = useRef<HTMLElement | null>(null)

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (
        !(
          e.key.toLowerCase() === shortcut ||
          (e.key.toLowerCase() === alternateShortcut && e.shiftKey)
        ) ||
        !(e.metaKey || e.ctrlKey)
      )
        return
      // The app owns this chord whether or not it acts on it: left to the
      // browser, it opens the address bar over whatever is on screen.
      e.preventDefault()
      // A form is a modal step out of the palette; do not stack one on top.
      // The palette itself is the exception, because this is also what
      // closes it. A terminal is not a modal, so its hidden textarea can
      // invoke the palette and receive focus back when it is dismissed.
      const s = useStore.getState()
      if (s.paletteDialog || templates) return
      if (!s.paletteOpen && inModal(e.target)) return
      // Capture the chord before xterm's target handler and keep it from
      // becoming terminal input. Guarded modal events deliberately continue
      // through their normal target path.
      e.stopPropagation()
      toggle()
    }
    window.addEventListener('keydown', onKey, { capture: true })
    return () => window.removeEventListener('keydown', onKey, { capture: true })
  }, [toggle, templates])

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
          invoker.current =
            target instanceof HTMLElement && target !== document.body ? target : null
        }}
        onCloseAutoFocus={(event) => {
          const destinationOpen = useStore.getState().paletteDialog !== null || templates
          if (!restoreFocus.current || destinationOpen) {
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
        />
      </CommandDialog>
      {templates && <TemplateDialog onClose={() => setTemplates(false)} />}
    </>
  )
}
