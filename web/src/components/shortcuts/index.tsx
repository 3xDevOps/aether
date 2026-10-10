import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { SectionLabel } from '@/components/ui/section-label'
import { useReturnFocus } from '@/lib/hooks'
import type { KeyScope } from '@/lib/key-scope'
import {
  formatKeys,
  isSingleKey,
  keybindings,
  listed,
  shortcutLabel,
  useKeybindings,
} from '@/lib/keybindings'
import { useStore } from '@/store'

const scopeNames: Record<KeyScope, string> = {
  global: 'Everywhere',
  run: 'In a run',
  card: 'On a focused board card',
  request: 'On a request card',
  composer: 'In the composer',
}

/** The keybinding table, grouped by scope, minus what does nothing here. */
function bindingGroups(singleKeys: boolean): { name: string; entries: [string, string][] }[] {
  return (Object.keys(scopeNames) as KeyScope[])
    .map((scope) => ({
      name: scopeNames[scope],
      entries: keybindings
        .filter((binding) => binding.scope === scope && listed(binding))
        .filter((binding) => singleKeys || !isSingleKey(binding))
        .map((binding): [string, string] => [shortcutLabel(binding.id), binding.label]),
    }))
    .filter((group) => group.entries.length > 0)
}

// Keys the focused control owns itself, outside the table.
const localGroups: { name: string; entries: [string, string][] }[] = [
  {
    name: 'When a tab strip or a resize handle has focus',
    entries: [
      [
        'Left / Right, Home / End',
        'Move along a tab strip; Enter or Space opens the focused tab',
      ],
      [
        'Arrow keys',
        'Resize the sidebar or a terminal 16px a press; Home and End are its limits, Enter collapses it',
      ],
    ],
  },
  {
    name: 'Changes (on a line number that has focus)',
    entries: [
      ['Up / Down', 'Move between the lines of a file'],
      ['Shift+Up / Down', 'Select a range of lines'],
      ['Enter', 'Comment on the line or the range'],
      [formatKeys('$mod+Enter'), 'Pin the comment being written; Esc closes an empty one'],
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
        `${formatKeys('$mod+=')} and ${formatKeys('$mod+-')} resize every terminal; ` +
          `${formatKeys('$mod+0')} restores the default`,
      ],
    ],
  },
]

function ShortcutKeys({ value }: { value: string }) {
  const parts = value.split(/\+|\s+then\s+/)
  return (
    <span className="flex min-w-0 max-w-full flex-wrap items-center gap-1 break-words" aria-label={value}>
      {parts.map((part, index) => (
        <span key={`${part}-${index}`} className="flex min-w-0 max-w-full flex-wrap items-center gap-1 break-words">
          {index > 0 && (
            <span aria-hidden className="text-ui-xs text-muted">
              {value.includes('then') ? 'then' : '+'}
            </span>
          )}
          <kbd className="inline-flex min-h-[22px] min-w-0 max-w-full items-center justify-center break-words rounded-control border border-seam bg-chrome px-1 font-code text-ui-xs font-medium leading-4 text-text">
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
      className="grid min-w-0 grid-cols-[minmax(0,14rem)_minmax(0,1fr)] items-start gap-x-3 gap-y-0.5 rounded-control px-2 py-1 odd:bg-chrome/30 max-[479px]:grid-cols-1"
    >
      <ShortcutKeys value={value} />
      <span className="min-w-0 text-ui leading-5 text-muted">
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
      <SectionLabel as="h3" id={`shortcut-${name}`} className="px-2">
        {name}
      </SectionLabel>
      <div role="list" className="space-y-px">
        {entries.map(([value, description]) => (
          <ShortcutRow key={value} value={value} description={description} />
        ))}
      </div>
    </section>
  )
}

export function ShortcutsDialog() {
  const open = useStore((s) => s.shortcutsOpen)
  const setOpen = useStore((s) => s.setShortcutsOpen)
  const singleKeys = useStore((s) => s.singleKeyShortcuts)
  useKeybindings('global', { shortcuts: () => setOpen(true) })
  const returnFocus = useReturnFocus()

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent
        className="max-h-[calc(100dvh-2rem)] max-w-[min(680px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)] gap-0 overflow-hidden p-0"
        onCloseAutoFocus={returnFocus.onCloseAutoFocus}
        onOpenAutoFocus={(event) => {
          returnFocus.onOpenAutoFocus()
          event.preventDefault()
          ;(event.currentTarget as HTMLElement).focus()
        }}
      >
        <DialogHeader className="min-w-0 border-b px-3 py-3 pr-10 sm:px-4">
          <DialogTitle>Keyboard shortcuts</DialogTitle>
          <DialogDescription>
            Single keys work when no field is focused. Shortcuts with Ctrl or Cmd also work in the terminal. Every command is in the command palette ({shortcutLabel('palette')}).
            {!singleKeys && ' Single-key shortcuts are off in Settings > Appearance.'}
          </DialogDescription>
        </DialogHeader>
        <div
          tabIndex={0}
          className="min-h-0 min-w-0 space-y-2 overflow-y-auto px-2 py-2 outline-none focus-visible:outline-1 focus-visible:-outline-offset-1 focus-visible:outline-seam sm:px-3"
        >
          {[...bindingGroups(singleKeys), ...localGroups].map((group) => (
            <ShortcutGroup key={group.name} {...group} />
          ))}
        </div>
      </DialogContent>
    </Dialog>
  )
}
