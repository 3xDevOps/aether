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
import { Tooltip } from '@/components/ui/heroui'
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

function ShortcutKeys({ value }: { value: string }) {
  const parts = value.split(/\+|\s+then\s+/)
  return (
    <span className="flex max-w-full shrink-0 flex-wrap items-center gap-1" aria-label={value}>
      {parts.map((part, index) => (
        <span key={`${part}-${index}`} className="flex max-w-full items-center gap-1">
          {index > 0 && (
            <span aria-hidden className="text-[11px] text-muted-foreground">
              {value.includes('then') ? 'then' : '+'}
            </span>
          )}
          <kbd className="inline-flex min-h-6 max-w-full items-center justify-center rounded-sm border border-border bg-muted px-1.5 py-1 font-mono text-[11px] font-medium leading-4 text-foreground shadow-[inset_0_-1px_0_hsl(var(--border))]">
            {part}
          </kbd>
        </span>
      ))}
    </span>
  )
}

function ShortcutRow({ value, description }: { value: string; description: string }) {
  return (
    <div
      role="listitem"
      className="flex flex-col items-start gap-2 rounded-md px-2 py-2.5 transition-colors odd:bg-muted/30 sm:flex-row sm:items-start sm:gap-4"
    >
      <ShortcutKeys value={value} />
      <span className="min-w-0 flex-1 text-[13px] leading-5 text-muted-foreground">
        {description}
      </span>
    </div>
  )
}

function ShortcutGroup({
  name,
  entries,
}: {
  name: string
  entries: [string, string][]
}) {
  return (
    <section className="space-y-1" aria-labelledby={`shortcut-${name}`}>
      <h3
        id={`shortcut-${name}`}
        className="px-2 text-xs font-semibold uppercase tracking-[0.08em] text-muted-foreground"
      >
        {name}
      </h3>
      <div role="list" className="space-y-0.5">
        {entries.map(([value, description]) => (
          <ShortcutRow key={value} value={value} description={description} />
        ))}
      </div>
    </section>
  )
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
      <Tooltip>
        <Tooltip.Trigger<'button'>
          render={(triggerProps) => (
            <button
              {...triggerProps}
              type="button"
              onClick={() => {
                setOpen(true)
              }}
              aria-label="Keyboard shortcuts"
              className={cn(
                focusRing,
                'flex items-center gap-1.5 rounded-md px-2 py-1 text-[13px] transition-colors hover:bg-accent hover:text-foreground',
              )}
            >
              <CircleHelp className="size-3.5" aria-hidden />
              <span className="hidden lg:inline">Shortcuts</span>
            </button>
          )}
        />
        <Tooltip.Content>Keyboard shortcuts</Tooltip.Content>
      </Tooltip>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-h-[calc(100dvh-2rem)] max-w-[min(640px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)] overflow-hidden">
          <DialogHeader>
            <DialogTitle>Keyboard shortcuts</DialogTitle>
            <DialogDescription>
              Keep your hands on the workbench. Shortcuts yield to focused
              fields and open dialogs.
            </DialogDescription>
          </DialogHeader>
          <div className="min-h-0 space-y-6 overflow-y-auto -mx-1 px-1">
            <section aria-labelledby="shortcut-shell" className="space-y-1">
              <h3
                id="shortcut-shell"
                className="px-2 text-xs font-semibold uppercase tracking-[0.08em] text-muted-foreground"
              >
                Shell
              </h3>
              <div role="list" className="space-y-0.5">
                {shellKeys(launchable).map(([key, what]) => (
                  <ShortcutRow key={key} value={key} description={what} />
                ))}
              </div>
            </section>
            {commandGroups().map((group) => (
              <ShortcutGroup key={group.name} {...group} />
            ))}
          </div>
        </DialogContent>
      </Dialog>
    </>
  )
}

registerSlot('statusbar', 'shortcuts', ShortcutsButton)
