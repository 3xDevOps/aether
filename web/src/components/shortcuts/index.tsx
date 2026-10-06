import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import type { KeyScope } from '@/lib/key-scope'
import {
  formatKeys,
  isSingleKey,
  keybindings,
  listed,
  shortcutLabel,
  useKeybindings,
} from '@/lib/keybindings'
import { cn, focusRing } from '@/lib/utils'
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
            <span aria-hidden className="text-[11px] text-muted-foreground">
              {value.includes('then') ? 'then' : '+'}
            </span>
          )}
          <kbd className="inline-flex min-h-[22px] min-w-0 max-w-full items-center justify-center break-words rounded-sm border border-border bg-muted px-1 font-mono text-[11px] font-medium leading-4 text-foreground">
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
      className="grid min-w-0 grid-cols-[minmax(0,14rem)_minmax(0,1fr)] items-start gap-x-3 gap-y-0.5 rounded-sm px-2 py-1 odd:bg-muted/30 max-[479px]:grid-cols-1"
    >
      <ShortcutKeys value={value} />
      <span className="min-w-0 text-[13px] leading-5 text-muted-foreground">
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
        className="px-2 text-[11px] font-semibold uppercase tracking-[0.08em] text-muted-foreground"
      >
        {name}
      </h3>
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

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] max-w-[min(680px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)] gap-0 overflow-hidden p-0">
        <DialogHeader className="min-w-0 border-b px-3 py-3 pr-10 sm:px-4">
          <DialogTitle>Keyboard shortcuts</DialogTitle>
          <DialogDescription>
            Unmodified shortcuts yield to focused fields. Modified shortcuts work from the terminal; dialogs and menus keep their own keys. Every command is in the command palette ({shortcutLabel('palette')}).
            {!singleKeys && ' Single-key shortcuts are off in Settings > Appearance.'}
          </DialogDescription>
        </DialogHeader>
        <div
          tabIndex={0}
          className={cn(
            focusRing,
            'focus-visible:-outline-offset-2 min-h-0 min-w-0 space-y-2 overflow-y-auto px-2 py-2 sm:px-3',
          )}
        >
          {[...bindingGroups(singleKeys), ...localGroups].map((group) => (
            <ShortcutGroup key={group.name} {...group} />
          ))}
        </div>
      </DialogContent>
    </Dialog>
  )
}
