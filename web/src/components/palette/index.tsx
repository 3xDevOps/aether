// The command palette. It has no home of its own in the shell, so it rides
// the status bar's extension slot: the trigger sits in the status bar and the
// palette itself is a dialog portalled to the document.

import { useEffect, useState } from 'react'
import { InjectDialog } from '@/components/palette/inject-dialog'
import { LaunchDialog } from '@/components/palette/launch-dialog'
import { PaletteBody } from '@/components/palette/palette'
import { TemplateDialog } from '@/components/palette/template-dialog'
import { registerSlot } from '@/components/slots'
import { CommandDialog } from '@/components/ui/command'
import { useStore } from '@/store'

const shortcut = 'k'

export function CommandPalette() {
  const open = useStore((s) => s.paletteOpen)
  const toggle = useStore((s) => s.togglePalette)
  const dialog = useStore((s) => s.paletteDialog)
  // The template form is not one of the store's palette dialogs; its open
  // state lives here with the other dialog hosts.
  const [templates, setTemplates] = useState(false)

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() !== shortcut || !(e.metaKey || e.ctrlKey)) return
      e.preventDefault()
      // A form is a modal step out of the palette; do not stack one on top.
      if (!useStore.getState().paletteDialog && !templates) toggle()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [toggle, templates])

  return (
    <>
      <button
        type="button"
        onClick={() => toggle(true)}
        title="Command palette"
        className="flex items-center gap-1 rounded px-1 hover:text-foreground"
      >
        <kbd className="rounded border px-1 font-sans text-[10px]">⌘K</kbd>
        Commands
      </button>
      <CommandDialog
        open={open}
        onOpenChange={(next: boolean) => toggle(next)}
        title="Command palette"
        description="Jump to a run or workspace, steer a run, launch a new one."
      >
        <PaletteBody
          onDone={() => toggle(false)}
          onTemplates={() => setTemplates(true)}
        />
      </CommandDialog>
      {dialog === 'launch' && <LaunchDialog />}
      {dialog === 'inject' && <InjectDialog />}
      {templates && <TemplateDialog onClose={() => setTemplates(false)} />}
    </>
  )
}

registerSlot('statusbar', 'palette', CommandPalette)
