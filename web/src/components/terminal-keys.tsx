// Every key goes through `terminal.input`, the entry xterm's own textarea uses,
// so the replay gate, `disableStdin` and the sticky modifiers apply to a tap
// as to a keystroke.

import { useState } from 'react'
import type * as React from 'react'
import { ArrowDown, ArrowLeft, ArrowRight, ArrowUp, CornerDownLeft, Ellipsis, Keyboard } from '@/components/icons'
import type { ModifierKey } from '@/components/terminal-modifiers'
import type { XtermController } from '@/components/xterm-host'
import { Button } from '@/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { maxTerminalFontSize, minTerminalFontSize } from '@/lib/term-font'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'

interface Key { label: string; bytes: string; icon?: React.ReactNode; name?: string }

const rowKeys: Key[] = [
  { label: 'Esc', bytes: '\x1b' },
  { label: 'Tab', bytes: '\t' },
  { label: 'Left', bytes: '\x1b[D', icon: <ArrowLeft /> },
  { label: 'Down', bytes: '\x1b[B', icon: <ArrowDown /> },
  { label: 'Up', bytes: '\x1b[A', icon: <ArrowUp /> },
  { label: 'Right', bytes: '\x1b[C', icon: <ArrowRight /> },
  { label: 'Enter', bytes: '\r', icon: <CornerDownLeft /> },
]

const moreKeys: Key[] = [
  ...['|', '~', '/', '\\', '-', '_', '`', '^'].map((symbol) => ({ label: symbol, bytes: symbol })),
  { label: 'Ctrl+C', bytes: '\x03', name: '^C' },
  { label: 'Home', bytes: '\x1b[H' },
  { label: 'End', bytes: '\x1b[F' },
  { label: 'PgUp', bytes: '\x1b[5~' },
  { label: 'PgDn', bytes: '\x1b[6~' },
  { label: 'Del', bytes: '\x1b[3~' },
  ...['P', 'Q', 'R', 'S'].map((final, index) => ({ label: `F${index + 1}`, bytes: `\x1bO${final}` })),
  ...[15, 17, 18, 19, 20, 21, 23, 24].map((code, index) => ({ label: `F${index + 5}`, bytes: `\x1b[${code}~` })),
]

/**
 * One whole pixel either way from the drawn size, which a phone's fit can
 * leave between two pixels, kept inside the zoom range.
 */
export function textSizeSteps(drawn: number) {
  return {
    smaller: Math.max(minTerminalFontSize, Math.ceil(drawn) - 1),
    larger: Math.min(maxTerminalFontSize, Math.floor(drawn) + 1),
  }
}

/** Taking focus would drop the soft keyboard between two keys. */
const keepFocus = (event: React.PointerEvent) => event.preventDefault()

function KeyButton({
  label,
  pressed,
  disabled,
  className,
  onPress,
  children,
}: {
  label: string
  pressed?: boolean
  disabled?: boolean
  className?: string
  onPress: () => void
  children: React.ReactNode
}) {
  return (
    <Button
      type="button"
      size="sm"
      variant={pressed ? 'primary' : 'secondary'}
      aria-label={label}
      aria-pressed={pressed}
      disabled={disabled}
      className={cn('h-11 min-w-0 px-0 font-code text-ui coarse:h-11', className)}
      onPointerDown={keepFocus}
      onClick={onPress}
    >
      {children}
    </Button>
  )
}

export function TerminalKeys({
  controller,
  fontSize,
  className,
}: {
  controller: XtermController
  /** The size the terminal is drawn at, for the sheet's A- and A+. */
  fontSize: number
  className?: string
}) {
  const { terminal, modifiers, tapModifier, focusTerminal } = controller
  const [more, setMore] = useState(false)
  const setFontSize = useStore((state) => state.setTerminalFontSize)
  const fitWidth = useStore((state) => state.terminalFitWidth)
  const fitTerminalWidth = useStore((state) => state.fitTerminalWidth)
  const { smaller, larger } = textSizeSteps(fontSize)
  // On a phone focusTerminal does nothing, so a key taps through without
  // raising the soft keyboard; a tablet keeps typing where the key left it.
  const send = (bytes: string) => {
    terminal?.input(bytes)
    focusTerminal()
  }
  const modifier = (key: ModifierKey, label: string) => (
    <KeyButton
      label={modifiers[key] === 'locked' ? `${label} locked` : label}
      pressed={modifiers[key] !== 'off'}
      className={cn('flex-1', modifiers[key] === 'locked' && 'underline decoration-2 underline-offset-4')}
      onPress={() => tapModifier(key)}
    >
      {label}
    </KeyButton>
  )
  const keyboardOpen = terminal?.textarea !== undefined && terminal.textarea === document.activeElement
  return (
    <div
      role="toolbar"
      aria-label="Terminal keys"
      className={cn(
        'flex shrink-0 items-center gap-0.5 border-t border-seam bg-chrome px-1 py-1 pb-[calc(0.25rem+var(--keyboard-inset,0px))]',
        className,
      )}
    >
      {modifier('ctrl', 'Ctrl')}
      {modifier('alt', 'Alt')}
      {rowKeys.map((key) => (
        <KeyButton key={key.label} label={key.label} className="flex-1" onPress={() => send(key.bytes)}>
          {key.icon ?? key.label}
        </KeyButton>
      ))}
      <Popover open={more} onOpenChange={setMore} modal={false}>
        <PopoverTrigger asChild>
          <Button
            type="button"
            size="sm"
            variant={more ? 'primary' : 'secondary'}
            aria-label="More keys"
            className="h-11 min-w-0 flex-1 px-0 coarse:h-11"
            onPointerDown={keepFocus}
          >
            <Ellipsis />
          </Button>
        </PopoverTrigger>
        <PopoverContent
          side="top"
          align="end"
          aria-label="More keys"
          className="grid w-[min(360px,calc(100vw-16px))] grid-cols-6 gap-1 p-2"
          onOpenAutoFocus={(event) => event.preventDefault()}
          onCloseAutoFocus={(event) => event.preventDefault()}
        >
          <KeyButton label="Smaller text" className="col-span-2" disabled={smaller >= fontSize} onPress={() => setFontSize(smaller)}>A−</KeyButton>
          <KeyButton label="Larger text" className="col-span-2" disabled={larger <= fontSize} onPress={() => setFontSize(larger)}>A+</KeyButton>
          <KeyButton label="Fit to width" className="col-span-2" disabled={fitWidth} onPress={fitTerminalWidth}>Fit</KeyButton>
          {moreKeys.map((key) => (
            <KeyButton
              key={key.label}
              label={key.label}
              onPress={() => {
                setMore(false)
                send(key.bytes)
              }}
            >
              {key.name ?? key.label}
            </KeyButton>
          ))}
          <Button
            type="button"
            size="sm"
            variant="secondary"
            className="col-span-6 h-11 coarse:h-11"
            onPointerDown={keepFocus}
            onClick={() => {
              setMore(false)
              if (keyboardOpen) terminal?.blur()
              else terminal?.focus()
            }}
          >
            <Keyboard />
            {keyboardOpen ? 'Hide keyboard' : 'Show keyboard'}
          </Button>
        </PopoverContent>
      </Popover>
    </div>
  )
}
