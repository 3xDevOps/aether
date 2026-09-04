// The keyboard shortcut reference. Like the palette it has no home of its
// own, so it rides the status bar slot: a small "?" trigger there, and the
// reference itself is a dialog portalled to the document. Shift+/ opens it
// from anywhere, unless a field has focus or a dialog is already up.

import { CircleHelp } from 'lucide-react'
import { useEffect, useState } from 'react'
import { registerSlot } from '@/components/slots'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { useStore } from '@/store'

// The verb table below is static prose, not a registry crawl: the verbs live
// in lib/commands.ts and this table is maintained alongside it. A new group
// of commands earns a row here.
const groups: { name: string; entries: [string, string][] }[] = [
  {
    name: 'Steer the focused run',
    entries: [
      ['Pause / Resume', 'Suspend or continue the run the centre view shows'],
      ['Inject a message', 'Send guidance to the running agent'],
      ['Close as merged / abandoned', 'Finish the run and record how it ended'],
      ['Kill run', 'Stop the run immediately'],
      ['Delete run', 'Remove the run, checkout and transcript'],
      ['Protect / Unprotect', 'Shield the run from the idle reaper'],
      ['Relaunch run', 'Start a finished run over from its task'],
      ['Pull branch', 'Fetch the run branch into the local workspace'],
      ['Hand off', 'Reassign the run to another member'],
    ],
  },
  {
    name: 'Go to',
    entries: [
      ['Members, Workspaces, Templates, Agents', 'Admin surfaces, when the gateway serves them'],
      ['Settings, Onboarding', 'Local gateway surfaces, when a link is configured'],
    ],
  },
  {
    name: 'Board',
    entries: [
      ['Open the run board / list', 'Jump between the board and the flat overview'],
      ['Launch a run / from a template', 'Start new work'],
      ['Mark all runs seen', 'Clear the attention markers'],
    ],
  },
]

/** True when the key event happened inside a text field of any flavour. */
function inField(e: KeyboardEvent): boolean {
  const t = e.target
  return (
    t instanceof HTMLElement &&
    (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)
  )
}

export function ShortcutsButton() {
  const [open, setOpen] = useState(false)

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== '?' || e.metaKey || e.ctrlKey || e.altKey) return
      // Typing "?" into a field is text, not a command; and a dialog already
      // on screen keeps the keyboard - same guard as the palette's.
      if (inField(e)) return
      const s = useStore.getState()
      if (s.paletteOpen || s.paletteDialog) return
      e.preventDefault()
      setOpen(true)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        title="Keyboard shortcuts"
        className="flex items-center gap-1 rounded px-1 hover:text-foreground"
      >
        <CircleHelp className="size-3.5" />
      </button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Keyboard shortcuts</DialogTitle>
            <DialogDescription>
              Every verb below is also a button on the surface it acts on. The
              palette is the fast path to all of them.
            </DialogDescription>
          </DialogHeader>
          <table className="w-full text-sm">
            <tbody>
              <tr>
                <td className="py-1 pr-4">
                  <kbd className="rounded border px-1 font-sans text-[10px]">⌘K / Ctrl+K</kbd>
                </td>
                <td className="py-1 text-muted-foreground">Open the command palette</td>
              </tr>
              <tr>
                <td className="py-1 pr-4">
                  <kbd className="rounded border px-1 font-sans text-[10px]">Shift+/</kbd>
                </td>
                <td className="py-1 text-muted-foreground">Open this reference</td>
              </tr>
            </tbody>
          </table>
          <div className="max-h-80 space-y-3 overflow-y-auto">
            {groups.map((g) => (
              <div key={g.name}>
                <div className="mb-1 text-xs font-medium text-muted-foreground">
                  {g.name}
                </div>
                <table className="w-full text-sm">
                  <tbody>
                    {g.entries.map(([verb, what]) => (
                      <tr key={verb}>
                        <td className="w-2/5 py-0.5 pr-4 align-top">{verb}</td>
                        <td className="py-0.5 text-muted-foreground">{what}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ))}
          </div>
        </DialogContent>
      </Dialog>
    </>
  )
}

registerSlot('statusbar', 'shortcuts', ShortcutsButton)
