// The rendered half of a terminal: the element xterm draws into, plus the
// find bar that searches its scrollback. Every terminal surface - the run
// terminal, the run-shell dock and the environment dock - renders this, so
// find behaves the same in all three.

import {
  ClipboardCopy,
  ClipboardPaste,
  ChevronDown,
  ChevronUp,
  Loader2,
  Minus,
  Plus,
  RotateCcw,
  Search,
  X,
} from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import type * as React from 'react'
import type { SearchAddon } from '@xterm/addon-search'
import type { XtermController } from '@/components/xterm-host'
import { Button } from '@/components/ui/button'
import { Tooltip } from '@/components/ui/heroui'
import { Input } from '@/components/ui/input'
import { copySelection, pasteClipboard } from '@/lib/term-clipboard'
import {
  defaultTerminalFontSize,
  maxTerminalFontSize,
  minTerminalFontSize,
} from '@/lib/term-font'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'

/** A terminal toolbar button and the shortcut its tooltip names. */
function ToolButton({
  hint,
  onClick,
  disabled,
  ...props
}: { hint: string } & React.ComponentProps<typeof Button>) {
  return (
    <Tooltip>
      <Tooltip.Trigger<'button'>
        render={(triggerProps) => (
          <Button
            {...triggerProps}
            {...props}
            aria-disabled={disabled || undefined}
            onClick={(event) => {
              if (disabled) return
              onClick?.(event)
            }}
          />
        )}
      />
      <Tooltip.Content>{hint}</Tooltip.Content>
    </Tooltip>
  )
}

function TerminalTools({ controller }: { controller: XtermController }) {
  const terminal = controller.terminal
  const fontSize = useStore((state) => state.terminalFontSize)
  const setFontSize = useStore((state) => state.setTerminalFontSize)

  return (
    <div
      role="toolbar"
      aria-label="Terminal controls"
      className="flex min-w-0 max-w-full flex-1 items-center gap-0.5 overflow-x-auto"
    >
      <ToolButton
        type="button"
        variant="ghost"
        size="icon"
        aria-label="Open terminal search"
        hint="Find in terminal (Ctrl+Shift+F)"
        onClick={() => controller.setFindOpen(true)}
      >
        <Search />
      </ToolButton>
      <span className="mx-0.5 hidden h-5 w-px bg-border sm:block" aria-hidden />
      <ToolButton
        type="button"
        variant="ghost"
        size="icon"
        aria-label="Decrease terminal text size"
        hint="Decrease terminal text size (Ctrl+-)"
        disabled={!terminal || fontSize <= minTerminalFontSize}
        onClick={() => setFontSize(fontSize - 1)}
      >
        <Minus />
      </ToolButton>
      <span
        className="hidden min-w-8 text-center text-xs tabular-nums text-muted-foreground sm:inline"
        aria-label={`Terminal text size ${fontSize}px`}
      >
        {fontSize}
      </span>
      <ToolButton
        type="button"
        variant="ghost"
        size="icon"
        aria-label="Increase terminal text size"
        hint="Increase terminal text size (Ctrl+=)"
        disabled={!terminal || fontSize >= maxTerminalFontSize}
        onClick={() => setFontSize(fontSize + 1)}
      >
        <Plus />
      </ToolButton>
      <ToolButton
        type="button"
        variant="ghost"
        size="icon"
        aria-label="Reset terminal text size"
        hint={`Reset terminal text size to ${defaultTerminalFontSize}px (Ctrl+0)`}
        disabled={!terminal || fontSize === defaultTerminalFontSize}
        onClick={() => setFontSize(defaultTerminalFontSize)}
      >
        <RotateCcw />
      </ToolButton>
      <span className="mx-0.5 h-5 w-px bg-border" aria-hidden />
      <ToolButton
        type="button"
        variant="ghost"
        size="icon"
        aria-label="Copy terminal selection"
        hint="Copy terminal selection (Ctrl+Shift+C)"
        disabled={!terminal}
        onClick={() => {
          if (terminal) void copySelection(terminal)
        }}
      >
        <ClipboardCopy />
      </ToolButton>
      <ToolButton
        type="button"
        variant="ghost"
        size="icon"
        aria-label="Paste into terminal"
        hint="Paste into terminal (Ctrl+Shift+V)"
        disabled={!terminal}
        onClick={() => {
          if (terminal) void pasteClipboard(terminal)
        }}
      >
        <ClipboardPaste />
      </ToolButton>
    </div>
  )
}

function FindBar({
  search,
  onClose,
}: {
  search: SearchAddon | null
  onClose: () => void
}) {
  const [term, setTerm] = useState('')
  const [missing, setMissing] = useState(false)
  const input = useRef<HTMLInputElement>(null)

  useEffect(() => {
    input.current?.focus()
    input.current?.select()
  }, [])

  const find = (direction: 'next' | 'previous') => {
    if (!search || !term) {
      setMissing(false)
      return
    }
    const found = direction === 'next' ? search.findNext(term) : search.findPrevious(term)
    setMissing(!found)
  }

  return (
    <div
      role="search"
      aria-label="Find terminal output"
      className="flex min-h-8 min-w-0 w-full items-center gap-1 rounded-md border border-border/80 bg-card/95 p-1 shadow-md backdrop-blur-sm"
    >
      <Input
        ref={input}
        aria-label="Find in terminal"
        placeholder="Find"
        value={term}
        className="h-8 min-w-0 flex-1 sm:w-40"
        onChange={(event) => {
          setTerm(event.target.value)
          setMissing(false)
        }}
        onKeyDown={(event) => {
          if (event.key === 'Escape') {
            event.preventDefault()
            onClose()
            return
          }
          if (event.key !== 'Enter') return
          event.preventDefault()
          find(event.shiftKey ? 'previous' : 'next')
        }}
      />
      {missing && (
        <span
          role="status"
          className="shrink-0 whitespace-nowrap px-1 text-[13px] text-muted-foreground"
        >
          No matches
        </span>
      )}
      <Button
        type="button"
        variant="ghost"
        size="icon"
        aria-label="Find previous"
        onClick={() => find('previous')}
      >
        <ChevronUp />
      </Button>
      <Button
        type="button"
        variant="ghost"
        size="icon"
        aria-label="Find next"
        onClick={() => find('next')}
      >
        <ChevronDown />
      </Button>
      <Button type="button" variant="ghost" size="icon" aria-label="Close find" onClick={onClose}>
        <X />
      </Button>
    </div>
  )
}

export function TerminalPane({
  controller,
  className,
  children,
}: {
  controller: XtermController
  /** Extra classes for the terminal element itself. */
  className?: string
  /** Additional content drawn over the terminal, such as `TerminalSpinner`. */
  children?: React.ReactNode
}) {
  return (
    <div className="relative flex h-full min-h-0 flex-col">
      <div className="flex h-10 shrink-0 items-center border-b bg-card/45 px-2">
        {!controller.findOpen ? (
          <TerminalTools controller={controller} />
        ) : (
          <FindBar search={controller.search} onClose={() => controller.setFindOpen(false)} />
        )}
      </div>
      <div
        ref={controller.hostRef}
        className={cn('min-h-0 flex-1 bg-background p-2 text-foreground', className)}
      />
      {children}
    </div>
  )
}

/**
 * What covers a terminal that has nothing to draw yet, because the xterm host
 * is blank until an attach acks. Shared so the run terminal and the
 * environment dock wait the same way and only the words differ.
 */
export function TerminalSpinner({ label }: { label: string }) {
  return (
    <div
      role="status"
      className="absolute inset-0 z-20 flex items-center justify-center gap-2 bg-background text-sm text-muted-foreground"
    >
      <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-hidden />
      {label}
    </div>
  )
}
