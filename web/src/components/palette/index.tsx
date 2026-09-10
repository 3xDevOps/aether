// The command palette. It has no home of its own in the shell, so it rides
// the status bar's extension slot: the trigger sits in the status bar and the
// palette itself is a dialog portalled to the document. The launch, inject and
// forward forms are not here - they are the shell's, in `dialogs.tsx`, so a
// button on any surface can open one.

import { useEffect, useState } from 'react'
import { PaletteBody } from '@/components/palette/palette'
import { TemplateDialog } from '@/components/palette/template-dialog'
import { registerSlot } from '@/components/slots'
import { CommandDialog } from '@/components/ui/command'
import { inModal } from '@/lib/keys'
import { shortcutLabel } from '@/lib/platform'
import { cn, focusRing } from '@/lib/utils'
import { useStore } from '@/store'

const shortcut = 'k'

export function CommandPalette() {
  const open = useStore((s) => s.paletteOpen)
  const toggle = useStore((s) => s.togglePalette)
  // The template form is not one of the store's palette dialogs; its open
  // state lives here with the other dialog hosts.
  const [templates, setTemplates] = useState(false)

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() !== shortcut || !(e.metaKey || e.ctrlKey)) return
      // The app owns this chord whether or not it acts on it: left to the
      // browser, it opens the address bar over whatever is on screen.
      e.preventDefault()
      // A form is a modal step out of the palette; do not stack one on top.
      // The forms this one hosts are store state, and every other modal is
      // asked of the event. The palette itself is the exception, because this
      // is also what closes it. Deliberately not `keyboardBusy`: a terminal
      // is not a modal, and on a run screen its hidden textarea holds the
      // focus, so this is the way out of one.
      const s = useStore.getState()
      if (s.paletteDialog || templates) return
      if (!s.paletteOpen && inModal(e.target)) return
      toggle()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [toggle, templates])

  return (
    <>
      {/* The word drops out below lg, so the label rather than the button's
          content has to carry the accessible name, and it has to be that same
          word: voice control matches on what a member can read. */}
      <button
        type="button"
        onClick={() => toggle(true)}
        aria-label="Commands"
        title="Command palette"
        className={cn(
          focusRing,
          'flex items-center gap-1.5 rounded-md px-2 py-1 text-[13px] transition-colors hover:bg-accent hover:text-foreground',
        )}
      >
        <kbd className="inline-flex h-5 min-w-5 items-center justify-center rounded-sm border border-border bg-muted px-1 font-mono text-[10px] font-medium text-muted-foreground">
          {shortcutLabel('K')}
        </kbd>
        <span className="hidden lg:inline">Commands</span>
      </button>
      <CommandDialog
        open={open}
        onOpenChange={(next: boolean) => toggle(next)}
        title="Command palette"
        description="Jump to a run or workspace, steer a run, launch a new one."
        className="max-h-[calc(100dvh-2rem)] max-w-[min(600px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)] rounded-lg"
      >
        <PaletteBody
          onDone={() => toggle(false)}
          onTemplates={() => setTemplates(true)}
        />
      </CommandDialog>
      {templates && <TemplateDialog onClose={() => setTemplates(false)} />}
    </>
  )
}

registerSlot('statusbar', 'palette', CommandPalette)
