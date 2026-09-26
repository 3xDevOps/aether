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
  SlidersHorizontal,
  X,
} from 'lucide-react'
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import type * as React from 'react'
import type { XtermController } from '@/components/xterm-host'
import { Button } from '@/components/ui/button'
import { Tooltip } from '@/components/ui/heroui'
import { Input } from '@/components/ui/input'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import {
  TerminalImageAction,
  type TerminalImageController,
  useTerminalImage,
} from '@/components/terminal-image'
import { TerminalKeys } from '@/components/terminal-keys'
import { useTerminalPan } from '@/components/terminal-pan'
import { coarsePointer, phoneScreen, useMediaQuery } from '@/lib/hooks'
import { copyScreen, copySelection } from '@/lib/term-clipboard'
import {
  defaultTerminalFontSize,
  maxTerminalFontSize,
  minTerminalFontSize,
} from '@/lib/term-font'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'

export interface TerminalReadSurface {
  copySelection(): void
  copyScreen(): void
  findNext(term: string): boolean | Promise<boolean>
  findPrevious(term: string): boolean | Promise<boolean>
  cancelFind(): void
  focus(): void
}

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
            className="group-data-[expanded=true]/terminal-tools:w-full group-data-[expanded=true]/terminal-tools:justify-start group-data-[expanded=true]/terminal-tools:px-3"
            aria-disabled={disabled || undefined}
            onClick={(event) => {
              if (disabled) return
              onClick?.(event)
            }}
          >
            {props.children}
            <span className="hidden group-data-[expanded=true]/terminal-tools:inline">
              {props['aria-label']}
            </span>
          </Button>
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
  return <span className="hidden pr-1 text-[12px] text-muted-foreground coarse:inline group-data-[expanded=true]/terminal-tools:hidden">{children}</span>
}

function TerminalTools({
  controller,
  image,
  readingSurface,
  expanded = false,
}: {
  controller: XtermController
  image: TerminalImageController
  readingSurface?: React.RefObject<TerminalReadSurface | null>
  expanded?: boolean
}) {
  const terminal = controller.terminal
  const fontSize = useStore((state) => state.terminalFontSize)
  const setFontSize = useStore((state) => state.setTerminalFontSize)

  return (
    <div
      role="toolbar"
      aria-label="Terminal controls"
      data-expanded={expanded}
      className={cn(
        'group/terminal-tools flex min-w-32 max-w-full shrink gap-0.5',
        expanded ? 'flex-col items-stretch [&>span]:hidden' : 'items-center overflow-x-auto',
      )}
    >
      <ToolButton
        type="button"
        variant="ghost"
        size="icon"
        aria-label="Open terminal search"
        hint={readingSurface ? 'Find in loaded recorded output (Ctrl+Shift+F)' : 'Find in terminal (Ctrl+Shift+F)'}
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
          if (readingSurface) readingSurface.current?.copySelection()
          else if (terminal) void copySelection(terminal)
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
          if (readingSurface) readingSurface.current?.copyScreen()
          else if (terminal) void copyScreen(terminal)
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
        disabled={!terminal || !!readingSurface}
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
  onNavigate,
}: {
  search: (Pick<TerminalReadSurface, 'findNext' | 'findPrevious'> & { cancelFind?: () => void }) | null
  onClose: () => void
  onNavigate?: () => void
}) {
  const [term, setTerm] = useState('')
  const [missing, setMissing] = useState(false)
  const input = useRef<HTMLInputElement>(null)
  const request = useRef(0)

  useEffect(() => {
    input.current?.focus()
    input.current?.select()
  }, [])

  const find = async (direction: 'next' | 'previous') => {
    if (!search || !term) {
      setMissing(false)
      return
    }
    const revision = ++request.current
    const found = await (direction === 'next' ? search.findNext(term) : search.findPrevious(term))
    if (revision !== request.current) return
    if (found) onNavigate?.()
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
        className="h-[26px] min-w-0 flex-1 coarse:h-10 coarse:min-h-10 sm:w-40"
        onChange={(event) => {
          setTerm(event.target.value)
          request.current++
          search?.cancelFind?.()
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
  replaying = false,
  surface,
  readingSurface,
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
  /** Whether replay is still parsing and the terminal surface must stay hidden. */
  replaying?: boolean
  /** Run-only overlay; the shared xterm host keeps its measured geometry. */
  surface?: React.ReactNode
  /** Redirect tools and mute input while the visible surface is recorded output. */
  readingSurface?: React.RefObject<TerminalReadSurface | null>
}) {
  const image = useTerminalImage({
    terminal: controller.terminal,
    imageTarget,
    imageTargetKey,
    imageUploadEnabled: readingSurface ? false : imageUploadEnabled,
    focusTerminal: controller.focusTerminal,
  })
  const coarse = useMediaQuery(coarsePointer)
  const phone = useMediaQuery(phoneScreen)
  useTerminalPan(controller.terminal, phone && !replaying && !readingSurface)
  // A terminal that cannot take input holds no modifier: the key bar goes
  // with the write access it needed, and a Ctrl left armed across that
  // would turn the first character of the next turn at the keyboard into a
  // control code nobody pressed.
  const armCtrl = controller.armCtrl
  const terminal = controller.terminal
  useEffect(() => {
    if (!writable || readingSurface) armCtrl(false)
  }, [armCtrl, readingSurface, writable])
  useLayoutEffect(() => {
    if (!terminal) return
    // Authority and replay still suppress all input. Reading only blocks DOM
    // input: the live parser must be able to answer terminal queries.
    terminal.options.disableStdin = !writable || replaying
    if (!writable || replaying || readingSurface) terminal.blur()
    const host = terminal.element?.parentElement
    if (!readingSurface || !host) return
    const block = (event: Event) => {
      event.preventDefault()
      event.stopImmediatePropagation()
    }
    const blur = () => terminal.blur()
    const events = ['keydown', 'keypress', 'keyup', 'beforeinput', 'input', 'paste', 'compositionstart', 'compositionupdate', 'compositionend']
    for (const event of events) host.addEventListener(event, block, true)
    host.addEventListener('focus', blur, true)
    return () => {
      for (const event of events) host.removeEventListener(event, block, true)
      host.removeEventListener('focus', blur, true)
    }
  }, [terminal, writable, replaying, readingSurface])
  return (
    <div className="relative flex h-full min-h-0 flex-col overflow-hidden">
      <div className="flex min-h-9 shrink-0 flex-wrap items-center gap-x-2 gap-y-1 border-b border-border bg-sidebar px-2 coarse:min-h-12">
        {!controller.findOpen ? (
          phone ? (
            <Popover>
              <PopoverTrigger asChild>
                <Button variant="ghost" size="sm" aria-label="Terminal tools">
                  <SlidersHorizontal />
                  Tools
                </Button>
              </PopoverTrigger>
              <PopoverContent className="w-[min(300px,calc(100vw-16px))] p-1">
                <TerminalTools controller={controller} image={image} readingSurface={readingSurface} expanded />
              </PopoverContent>
            </Popover>
          ) : <TerminalTools controller={controller} image={image} readingSurface={readingSurface} />
        ) : (
          <FindBar
            search={readingSurface ? {
              findNext: (term) => readingSurface.current?.findNext(term) ?? false,
              findPrevious: (term) => readingSurface.current?.findPrevious(term) ?? false,
              cancelFind: () => readingSurface.current?.cancelFind(),
            } : controller.search}
            onNavigate={controller.noteViewportInteraction}
            onClose={() => {
              if (readingSurface) {
                readingSurface.current?.cancelFind()
                readingSurface.current?.focus()
              } else controller.focusTerminal()
              controller.setFindOpen(false)
            }}
          />
        )}
        {toolbarEnd}
      </div>
      <div className="relative flex min-h-0 min-w-0 flex-1">
      <div
        ref={controller.hostRef}
        inert={replaying || !!readingSurface}
        className={cn(
          'min-h-0 min-w-0 flex-1 overflow-x-auto overflow-y-hidden bg-background p-2 text-foreground',
          className,
        )}
        style={{
          overflowY: phone ? 'auto' : 'hidden',
          overscrollBehaviorY: 'none',
          visibility: replaying || readingSurface ? 'hidden' : undefined,
        }}
      />
        {surface}
      </div>
      {coarse && writable && !readingSurface && <TerminalKeys controller={controller} />}
      {children}
      {replaying && !readingSurface && (
        <div
          role="status"
          aria-label="Restoring terminal history"
          className="absolute inset-0 z-30 flex items-center justify-center bg-background text-[13px] text-muted-foreground"
        >
          Restoring terminal history
        </div>
      )}
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
      aria-label={label}
      className="absolute inset-0 z-20 flex items-center justify-center gap-2 bg-background text-[13px] text-muted-foreground"
    >
      <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-hidden />
      {label}
    </div>
  )
}
