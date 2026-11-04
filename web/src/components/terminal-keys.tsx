// Every key goes through `terminal.input`, the entry xterm's own textarea uses,
// so the replay gate, `disableStdin` and the sticky modifiers apply to a tap
// as to a keystroke.

import { useEffect, useRef, useState } from 'react'
import type * as React from 'react'
import { ArrowDown, ArrowLeft, ArrowRight, ArrowUp, CornerDownLeft, Ellipsis, Keyboard, Lock } from '@/components/icons'
import type { TerminalFit } from '@/components/terminal-fit'
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
]

const functionKeys: Key[] = [
  ...['P', 'Q', 'R', 'S'].map((final, index) => ({ label: `F${index + 1}`, bytes: `\x1bO${final}` })),
  ...[15, 17, 18, 19, 20, 21, 23, 24].map((code, index) => ({ label: `F${index + 5}`, bytes: `\x1b[${code}~` })),
]

/**
 * A phone's text size steps a whole pixel from the drawn size. Fitting is
 * the smallest size worth drawing, so a step down to it or below the zoom
 * range returns to fitting instead.
 */
export function useTextSizeStepper({ size, fitted }: TerminalFit) {
  const fitWidth = useStore((state) => state.terminalFitWidth)
  const chosen = useStore((state) => state.terminalFontSize)
  const setFontSize = useStore((state) => state.setTerminalFontSize)
  const fit = useStore((state) => state.fitTerminalWidth)
  // A grid that already fits at the chosen size, such as one sized to its pane, is not being fitted.
  const fitting = fitWidth && fitted < chosen
  const smaller = Math.ceil(size) - 1
  const larger = Math.min(maxTerminalFontSize, Math.floor(size) + 1)
  return {
    size: `${Number(size.toFixed(1))} px`,
    fitWidth: fitting,
    fit,
    canSmaller: !fitting && smaller >= minTerminalFontSize,
    smaller: () => (fitted < chosen && smaller <= fitted ? fit() : setFontSize(smaller)),
    canLarger: larger > size,
    larger: () => setFontSize(Math.max(minTerminalFontSize, larger)),
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
  label?: string
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

function SizeStepper({ fit }: { fit: TerminalFit }) {
  const stepper = useTextSizeStepper(fit)
  return (
    <>
      <KeyButton label="Smaller text" className="col-span-2" disabled={!stepper.canSmaller} onPress={stepper.smaller}>A−</KeyButton>
      <Button
        type="button"
        size="sm"
        variant="secondary"
        aria-label={`Text size ${stepper.size}, ${stepper.fitWidth ? 'fitted to width' : 'fit to width'}`}
        aria-disabled={stepper.fitWidth}
        className="col-span-3 h-11 min-w-0 px-0 text-ui coarse:h-11 aria-disabled:cursor-default aria-disabled:opacity-100"
        onPointerDown={keepFocus}
        onClick={() => {
          if (!stepper.fitWidth) stepper.fit()
        }}
      >
        {stepper.size} · {stepper.fitWidth ? 'fitted' : 'Fit'}
      </Button>
      <KeyButton label="Larger text" className="col-span-2" disabled={!stepper.canLarger} onPress={stepper.larger}>A+</KeyButton>
    </>
  )
}

/** How much of the visual viewport a soft keyboard takes, at least. */
const keyboardHeight = 120

/**
 * Whether the soft keyboard is up: the textarea has focus and something took
 * the bottom of the visual viewport. Focus alone is not enough, because
 * Android's Back hides the keyboard and leaves the textarea focused.
 */
function useKeyboardOpen(textarea: HTMLTextAreaElement | undefined) {
  const [open, setOpen] = useState(false)
  useEffect(() => {
    if (!textarea) return
    const viewport = window.visualViewport
    let tallest = viewport?.height ?? 0
    let width = viewport?.width ?? 0
    const update = () => {
      if (viewport) {
        // A rotation changes the height without a keyboard; start over.
        if (viewport.width !== width) {
          width = viewport.width
          tallest = viewport.height
        }
        tallest = Math.max(tallest, viewport.height)
      }
      const covered = viewport ? tallest - viewport.height >= keyboardHeight : true
      setOpen(document.activeElement === textarea && covered)
    }
    update()
    textarea.addEventListener('focus', update)
    textarea.addEventListener('blur', update)
    viewport?.addEventListener('resize', update)
    return () => {
      textarea.removeEventListener('focus', update)
      textarea.removeEventListener('blur', update)
      viewport?.removeEventListener('resize', update)
    }
  }, [textarea])
  return open
}

export function TerminalKeys({
  controller,
  fit,
  className,
}: {
  controller: XtermController
  /** A phone's fit, which the sheet's size stepper steps from. */
  fit?: TerminalFit
  className?: string
}) {
  const { terminal, modifiers, tapModifier, focusTerminal } = controller
  const [more, setMore] = useState(false)
  const [functions, setFunctions] = useState(false)
  const moreTrigger = useRef<HTMLButtonElement>(null)
  const keyboardOpen = useKeyboardOpen(terminal?.textarea)
  // On a phone focusTerminal does nothing, so a key taps through without
  // raising the soft keyboard; a tablet keeps typing where the key left it.
  const send = (bytes: string) => {
    // A full-screen program that asked for application cursor keys (DECCKM)
    // reads the arrows, Home and End as SS3, as a keyboard sends them.
    const cursor = terminal?.modes.applicationCursorKeysMode ? bytes.replace(/^\x1b\[([A-DHF])$/, '\x1bO$1') : bytes
    terminal?.input(cursor)
    focusTerminal()
  }
  const sheetKey = (key: Key) => (
    <KeyButton
      key={key.label}
      label={key.label}
      onPress={() => send(key.bytes)}
    >
      {key.name ?? key.label}
    </KeyButton>
  )
  const modifier = (key: ModifierKey, label: string) => (
    <KeyButton
      pressed={modifiers[key] !== 'off'}
      className="relative flex-1"
      onPress={() => tapModifier(key)}
    >
      {label}
      {modifiers[key] === 'locked' && <span className="sr-only">, locked</span>}
      {modifiers[key] === 'locked' && <Lock aria-hidden className="absolute top-1 right-1 size-2.5" />}
    </KeyButton>
  )
  return (
    <div
      role="toolbar"
      aria-label="Terminal keys"
      className={cn(
        'flex shrink-0 items-center gap-1 border-t border-seam bg-chrome px-1 py-1 pb-[calc(0.25rem+var(--keyboard-inset,0px))]',
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
            ref={moreTrigger}
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
          className="grid w-[min(360px,calc(100vw-16px))] grid-cols-7 gap-1 p-2"
          onOpenAutoFocus={(event) => {
            // Moving focus off a focused textarea would drop the keyboard.
            if (terminal?.textarea && document.activeElement === terminal.textarea) event.preventDefault()
          }}
          onCloseAutoFocus={(event) => {
            event.preventDefault()
            // The keyboard button hands focus to the terminal itself.
            if (document.activeElement === document.body || !document.activeElement) moreTrigger.current?.focus()
          }}
        >
          {moreKeys.map(sheetKey)}
          {functions && functionKeys.map(sheetKey)}
          <KeyButton label="Function keys" pressed={functions} className="col-span-3" onPress={() => setFunctions(!functions)}>
            F1–F12
          </KeyButton>
          <Button
            type="button"
            size="sm"
            variant="secondary"
            className="col-span-4 h-11 coarse:h-11"
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
          {fit && <SizeStepper fit={fit} />}
        </PopoverContent>
      </Popover>
    </div>
  )
}
