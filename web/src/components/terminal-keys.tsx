// The keys a soft keyboard does not have. A phone keyboard sends characters,
// so an agent TUI's Esc, Tab, arrows and Ctrl are unreachable without this
// bar. Every key goes through `terminal.input`, the same entry xterm's own
// textarea uses, so the replay gate and `disableStdin` apply to a tap exactly
// as they apply to a keystroke.

import type { Terminal } from '@xterm/xterm'
import { ArrowDown, ArrowLeft, ArrowRight, ArrowUp, CornerDownLeft } from 'lucide-react'
import type * as React from 'react'
import type { XtermController } from '@/components/xterm-host'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

/** The bytes a key sends, in the order they appear in the bar. */
const keys: { label: string; bytes: string; icon?: React.ReactNode }[] = [
  { label: 'Esc', bytes: '\x1b' },
  { label: 'Tab', bytes: '\t' },
  { label: 'Left', bytes: '\x1b[D', icon: <ArrowLeft /> },
  { label: 'Down', bytes: '\x1b[B', icon: <ArrowDown /> },
  { label: 'Up', bytes: '\x1b[A', icon: <ArrowUp /> },
  { label: 'Right', bytes: '\x1b[C', icon: <ArrowRight /> },
  { label: 'Enter', bytes: '\r', icon: <CornerDownLeft /> },
  { label: 'Ctrl+C', bytes: '\x03' },
]

function KeyButton({
  label,
  pressed,
  terminal,
  onPress,
  children,
}: {
  label: string
  pressed?: boolean
  terminal: Terminal | null
  onPress: () => void
  children: React.ReactNode
}) {
  return (
    <Button
      type="button"
      size="sm"
      variant={pressed ? 'default' : 'outline'}
      aria-label={label}
      aria-pressed={pressed}
      className="shrink-0 px-3 font-mono text-[13px]"
      // Taking focus would drop the soft keyboard between two keys.
      onPointerDown={(event) => event.preventDefault()}
      onClick={() => {
        onPress()
        terminal?.focus()
      }}
    >
      {children}
    </Button>
  )
}

export function TerminalKeys({
  controller,
  className,
}: {
  controller: XtermController
  className?: string
}) {
  const terminal = controller.terminal
  return (
    <div
      role="toolbar"
      aria-label="Terminal keys"
      className={cn(
        'flex shrink-0 items-center gap-1 overflow-x-auto border-t border-border bg-sidebar px-2 py-1 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden',
        className,
      )}
    >
      <KeyButton
        label="Ctrl"
        pressed={controller.ctrlArmed}
        terminal={terminal}
        onPress={() => controller.armCtrl(!controller.ctrlArmed)}
      >
        Ctrl
      </KeyButton>
      {keys.map((key) => (
        <KeyButton
          key={key.label}
          label={key.label}
          terminal={terminal}
          onPress={() => terminal?.input(key.bytes)}
        >
          {key.icon ?? key.label}
        </KeyButton>
      ))}
    </div>
  )
}
