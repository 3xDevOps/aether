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
import { canLaunch } from '@/lib/commands'
import { keyboardBusy } from '@/lib/keys'
import { shortcutLabel } from '@/lib/platform'
import { cn, focusRing } from '@/lib/utils'
import { useCapability, useSelfRole } from '@/store/hooks'

/**
 * The keys the shell itself listens for, as the reader has to press them.
 * `nav-shortcuts.ts` implements every row below the first two; a key added
 * there earns a row here.
 */
function shellKeys(launchable: boolean): [string, string][] {
  return [
    [shortcutLabel('K'), 'Open the command palette'],
    ['Shift+/', 'Open this reference'],
    ...(launchable ? ([['n', 'Launch a run']] as [string, string][]) : []),
    ['g then b', 'Go to the board'],
    ['g then l', 'Go to all runs'],
    ['Esc', 'Leave a run for the board'],
  ]
}

// The verb table below is static prose, not a registry crawl: the verbs live
// in lib/commands.ts and this table is maintained alongside it. A new group
// of commands earns a row here.
function commandGroups(): { name: string; entries: [string, string][] }[] {
  return [
    {
      name: 'Steer the focused run',
      entries: [
        ['Pause / Resume', 'Suspend or continue the run the centre view shows'],
        [
          'Send a message to the agent',
          'Send text into the run without attaching to it',
        ],
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
        [
          'Approvals, Activity, Members, Manage workspaces, Templates, Agents, Files',
          'Surfaces, when the gateway serves their methods',
        ],
        ['Onboarding, Settings', 'Local gateway surfaces, when a link is configured'],
      ],
    },
    {
      name: 'Board',
      entries: [
        ['Open the board / all runs', 'Jump between the board and the flat list'],
        ['Launch a run / from a template', 'Start new work'],
        ['Mark all runs seen', 'Clear the attention markers'],
      ],
    },
    {
      name: 'When a tab strip or a resize handle has focus',
      entries: [
        [
          'Left / Right, Home / End',
          'Move along a run or dock tab strip; Enter or Space opens the focused tab',
        ],
        [
          'Arrow keys',
          'Resize the sidebar or a dock 16px a press; Home and End are its limits, Enter collapses it',
        ],
      ],
    },
    {
      name: 'Terminal (in the terminal that has focus)',
      entries: [
        ['Copy', 'Ctrl+Shift+C - a plain Ctrl+C copies too when text is selected'],
        ['Paste', 'Ctrl+Shift+V - plain Ctrl+V works as well'],
        ['Find', 'Ctrl+Shift+F - Enter for the next match, Shift+Enter back, Esc closes'],
        [
          'Zoom',
          `${shortcutLabel('=')} and ${shortcutLabel('-')} resize every terminal; ` +
            `${shortcutLabel('0')} restores the default`,
        ],
      ],
    },
  ]
}

export function ShortcutsButton() {
  const [open, setOpen] = useState(false)
  // The same gate the handler answers to, so the reference never offers a key
  // that would do nothing.
  const launchable = canLaunch({ cap: useCapability(), role: useSelfRole() })

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== '?' || e.metaKey || e.ctrlKey || e.altKey) return
      if (e.defaultPrevented || keyboardBusy(e)) return
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
        aria-label="Keyboard shortcuts"
        title="Keyboard shortcuts"
        className={cn(focusRing, 'flex items-center gap-1 rounded px-1 hover:text-foreground')}
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
              {shellKeys(launchable).map(([key, what]) => (
                <tr key={key}>
                  <td className="py-1 pr-4">
                    <kbd className="rounded border px-1 font-sans text-[10px] whitespace-nowrap">
                      {key}
                    </kbd>
                  </td>
                  <td className="py-1 text-muted-foreground">{what}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="max-h-80 space-y-3 overflow-y-auto">
            {commandGroups().map((g) => (
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
