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
  ScanText,
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
import {
  TerminalImageAction,
  type TerminalImageController,
  useTerminalImage,
} from '@/components/terminal-image'
import { TerminalKeys } from '@/components/terminal-keys'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { copyScreen, copySelection } from '@/lib/term-clipboard'
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

/**
 * What a touch screen reads instead of a tooltip. A tooltip opens on hover,
 * which a finger never produces, so two actions that differ only by icon -
 * copying a selection and copying the screen - need the word beside them
 * where there is no pointer to hover with.
 */
function ToolLabel({ children }: { children: React.ReactNode }) {
  return <span className="hidden pr-1 text-[12px] text-muted-foreground coarse:inline">{children}</span>
}

function TerminalTools({
  controller,
  image,
}: {
  controller: XtermController
  image: TerminalImageController
}) {
  const terminal = controller.terminal
  const fontSize = useStore((state) => state.terminalFontSize)
  const setFontSize = useStore((state) => state.setTerminalFontSize)

  return (
    <div
      role="toolbar"
      aria-label="Terminal controls"
      className="flex min-w-32 max-w-full shrink items-center gap-0.5 overflow-x-auto"
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
      <span className="mx-0.5 hidden h-4 w-px bg-border sm:block" aria-hidden />
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
      <span className="mx-0.5 h-4 w-px bg-border" aria-hidden />
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
      <ToolLabel>Selection</ToolLabel>
      <ToolButton
        type="button"
        variant="ghost"
        size="icon"
        aria-label="Copy last screen"
        hint="Copy the rows on screen"
        disabled={!terminal}
        onClick={() => {
          if (terminal) void copyScreen(terminal)
        }}
      >
        <ScanText />
      </ToolButton>
      <ToolLabel>Screen</ToolLabel>
      <ToolButton
        type="button"
        variant="ghost"
        size="icon"
        aria-label="Paste into terminal"
        hint="Paste into terminal (Ctrl+Shift+V)"
        disabled={!terminal}
        onClick={() => {
          if (terminal) void image.pasteClipboard()
        }}
      >
        <ClipboardPaste />
      </ToolButton>
      <TerminalImageAction controller={image} />
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
      className="flex h-8 min-h-8 min-w-32 flex-[1_1_20rem] items-center gap-1 border border-input bg-background px-1 coarse:h-11 coarse:min-h-11"
    >
      <Input
        ref={input}
        aria-label="Find in terminal"
        placeholder="Find"
        value={term}
        className="h-[26px] min-w-0 flex-1 coarse:h-10 sm:w-40"
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
  toolbarEnd,
  imageTarget,
  imageTargetKey,
  imageUploadEnabled,
  writable = true,
}: {
  controller: XtermController
  /** Extra classes for the terminal element itself. */
  className?: string
  /** Additional content drawn over the terminal, such as `TerminalSpinner`. */
  children?: React.ReactNode
  /** Controls and state placed after the terminal tools in their shared strip. */
  toolbarEnd?: React.ReactNode
  /** Run ID for a run terminal or shell; omit for the member environment. */
  imageTarget?: string
  /** Active terminal identity, used to reject late uploads after tab changes. */
  imageTargetKey?: string
  /** Whether this attached terminal may accept an uploaded path. */
  imageUploadEnabled?: boolean
  /** Whether what is typed here reaches the shell. A mirror shows no keys. */
  writable?: boolean
}) {
  const image = useTerminalImage({
    terminal: controller.terminal,
    imageTarget,
    imageTargetKey,
    imageUploadEnabled,
    focusTerminal: controller.focusTerminal,
  })
  const coarse = useMediaQuery(coarsePointer)
  // A terminal that cannot take input holds no modifier: the key bar goes
  // with the write access it needed, and a Ctrl left armed across that
  // would turn the first character of the next turn at the keyboard into a
  // control code nobody pressed.
  const armCtrl = controller.armCtrl
  useEffect(() => {
    if (!writable) armCtrl(false)
  }, [armCtrl, writable])
  return (
    <div className="relative flex h-full min-h-0 flex-col overflow-hidden">
      <div className="flex min-h-9 shrink-0 flex-wrap items-center gap-x-2 gap-y-1 border-b border-border bg-sidebar px-2 coarse:min-h-12">
        {!controller.findOpen ? (
          <TerminalTools controller={controller} image={image} />
        ) : (
          <FindBar
            search={controller.search}
            onClose={() => {
              controller.focusTerminal()
              controller.setFindOpen(false)
            }}
          />
        )}
        {toolbarEnd}
      </div>
      <div
        ref={controller.hostRef}
        className={cn('min-h-0 flex-1 overflow-hidden bg-background p-2 text-foreground', className)}
      />
      {coarse && writable && <TerminalKeys controller={controller} />}
      {children}
      {image.dialog}
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
      className="absolute inset-0 z-20 flex items-center justify-center gap-2 bg-background text-[13px] text-muted-foreground"
    >
      <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-hidden />
      {label}
    </div>
  )
}
