import { useEffect, useLayoutEffect, useState, useRef } from 'react'
import type * as React from 'react'
import {
  Camera,
  ChevronDown,
  ChevronUp,
  ClipboardCopy,
  ClipboardPaste,
  ImageUp,
  LoaderCircle,
  Minus,
  Plus,
  RotateCcw,
  ScanText,
  Search,
  SlidersHorizontal,
  X,
} from '@/components/icons'
import type { XtermController } from '@/components/xterm-host'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Menu, MenuContent, MenuItem, MenuLabel, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { type TerminalImageController, useTerminalImage } from '@/components/terminal-image'
import { TerminalKeys } from '@/components/terminal-keys'
import { TerminalControlBorder, type TerminalControlAppearance } from '@/components/terminal-control-border'
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

/** Keeps the menu open so the size can be stepped more than once. */
const stay = (event: Event) => event.preventDefault()

/** The clipboard fallback focuses a helper field, which the open menu's focus trap would refuse. */
const afterClose = (action: () => void) => () => window.setTimeout(action)

function TerminalTools({
  controller,
  image,
  readingSurface,
  onCaptures,
}: {
  controller: XtermController
  image: TerminalImageController
  readingSurface?: React.RefObject<TerminalReadSurface | null>
  onCaptures?: (returnTo: HTMLElement | null) => void
}) {
  const trigger = useRef<HTMLButtonElement>(null)
  const terminal = controller.terminal
  const fontSize = useStore((state) => state.terminalFontSize)
  const setFontSize = useStore((state) => state.setTerminalFontSize)

  return (
    <Menu>
      <MenuTrigger asChild>
        <Button ref={trigger} variant="ghost" size="sm" aria-label="Terminal tools">
          <SlidersHorizontal />
          Tools
        </Button>
      </MenuTrigger>
      <MenuContent align="start" className="md:w-60">
        <MenuItem onSelect={() => controller.setFindOpen(true)}>
          <Search />
          {readingSurface ? 'Find in recorded output' : 'Find'}
          <span className="ml-auto text-ui-sm text-muted">Ctrl+Shift+F</span>
        </MenuItem>
        <MenuSeparator />
        <MenuLabel>Text size {fontSize}px</MenuLabel>
        <MenuItem disabled={!terminal || fontSize <= minTerminalFontSize} onSelect={(event) => { stay(event); setFontSize(fontSize - 1) }}>
          <Minus />
          Smaller text
          <span className="ml-auto text-ui-sm text-muted">Ctrl+-</span>
        </MenuItem>
        <MenuItem disabled={!terminal || fontSize >= maxTerminalFontSize} onSelect={(event) => { stay(event); setFontSize(fontSize + 1) }}>
          <Plus />
          Larger text
          <span className="ml-auto text-ui-sm text-muted">Ctrl+=</span>
        </MenuItem>
        <MenuItem disabled={!terminal || fontSize === defaultTerminalFontSize} onSelect={(event) => { stay(event); setFontSize(defaultTerminalFontSize) }}>
          <RotateCcw />
          Reset text size
          <span className="ml-auto text-ui-sm text-muted">Ctrl+0</span>
        </MenuItem>
        <MenuSeparator />
        <MenuItem
          disabled={!terminal}
          onSelect={afterClose(() => {
            if (readingSurface) readingSurface.current?.copySelection()
            else if (terminal) void copySelection(terminal)
          })}
        >
          <ClipboardCopy />
          Copy selection
        </MenuItem>
        <MenuItem
          disabled={!terminal}
          onSelect={afterClose(() => {
            if (readingSurface) readingSurface.current?.copyScreen()
            else if (terminal) void copyScreen(terminal)
          })}
        >
          <ScanText />
          Copy screen
        </MenuItem>
        <MenuItem disabled={!terminal || !!readingSurface} onSelect={afterClose(() => void image.pasteClipboard())}>
          <ClipboardPaste />
          Paste
        </MenuItem>
        <MenuItem disabled={!image.canUpload} onSelect={image.openPicker}>
          <ImageUp />
          Upload image…
        </MenuItem>
        {onCaptures && (
          <>
            <MenuSeparator />
            <MenuItem onSelect={() => onCaptures(trigger.current)}>
              <Camera />
              Captures…
            </MenuItem>
          </>
        )}
      </MenuContent>
    </Menu>
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
      className="flex min-w-32 flex-1 items-center gap-1"
    >
      <Input
        ref={input}
        aria-label="Find in terminal"
        placeholder="Find"
        value={term}
        className="min-w-0 flex-1 sm:max-w-64"
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
          className="shrink-0 whitespace-nowrap px-1 text-ui-sm text-muted"
        >
          No matches
        </span>
      )}
      <Button
        type="button"
        variant="ghost"
        size="icon-sm"
        label="Find previous"
        onClick={() => find('previous')}
      >
        <ChevronUp />
      </Button>
      <Button
        type="button"
        variant="ghost"
        size="icon-sm"
        label="Find next"
        onClick={() => find('next')}
      >
        <ChevronDown />
      </Button>
      <Button type="button" variant="ghost" size="icon-sm" label="Close find" onClick={onClose}>
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
  controlAppearance,
  takeoverProgress,
  tabs,
  notice,
  onCaptures,
}: {
  controller: XtermController
  className?: string
  children?: React.ReactNode
  toolbarEnd?: React.ReactNode
  /** Run ID for a run terminal or shell; omit for the member environment. */
  imageTarget?: string
  /** Active terminal identity, used to reject late uploads after tab changes. */
  imageTargetKey?: string
  imageUploadEnabled?: boolean
  /** Whether what is typed here reaches the shell. A mirror shows no keys. */
  writable?: boolean
  /** Whether replay is still parsing and the terminal surface must stay hidden. */
  replaying?: boolean
  /** Run-only overlay; the shared xterm host keeps its measured geometry. */
  surface?: React.ReactNode
  /** Redirect tools and mute input while the visible surface is recorded output. */
  readingSurface?: React.RefObject<TerminalReadSurface | null>
  /** Acknowledged local lease state; omitted on surfaces without shared control. */
  controlAppearance?: TerminalControlAppearance
  takeoverProgress?: number
  tabs?: React.ReactNode
  notice?: React.ReactNode
  onCaptures?: (returnTo: HTMLElement | null) => void
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
  // A Ctrl left armed after losing write access would turn the next typed
  // character into a control code nobody pressed.
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
  const closeFind = () => {
    if (readingSurface) {
      readingSurface.current?.cancelFind()
      readingSurface.current?.focus()
    } else controller.focusTerminal()
    controller.setFindOpen(false)
  }
  return (
    <div className="@container/terminal-pane relative flex h-full min-h-0 flex-col overflow-hidden">
      <div
        role="toolbar"
        aria-label="Terminal toolbar"
        className="relative z-10 flex h-8 shrink-0 items-center gap-1 border-b border-seam bg-chrome px-1 coarse:h-11"
      >
        {controller.findOpen ? (
          <FindBar
            search={readingSurface ? {
              findNext: (term) => readingSurface.current?.findNext(term) ?? false,
              findPrevious: (term) => readingSurface.current?.findPrevious(term) ?? false,
              cancelFind: () => readingSurface.current?.cancelFind(),
            } : controller.search}
            onNavigate={controller.noteViewportInteraction}
            onClose={closeFind}
          />
        ) : (
          <>
            {tabs}
            <TerminalTools controller={controller} image={image} readingSurface={readingSurface} onCaptures={onCaptures} />
            <div className="min-w-0 flex-1" />
            {toolbarEnd}
          </>
        )}
      </div>
      {notice}
      <div className="relative z-0 flex min-h-0 min-w-0 flex-1">
      <div
        ref={controller.hostRef}
        inert={replaying || !!readingSurface}
        className={cn(
          'min-h-0 min-w-0 flex-1 overflow-x-auto overflow-y-hidden bg-canvas p-2 text-text',
          className,
        )}
        style={{
          overflowY: phone ? 'auto' : 'hidden',
          overscrollBehaviorY: 'none',
          visibility: replaying || readingSurface ? 'hidden' : undefined,
        }}
      />
        {surface}
        {controlAppearance && (
          <TerminalControlBorder
            appearance={replaying || readingSurface ? 'hidden' : controlAppearance}
            takeoverProgress={takeoverProgress}
          />
        )}
      </div>
      {coarse && writable && !readingSurface && <TerminalKeys controller={controller} />}
      {children}
      {replaying && !readingSurface && (
        <div
          role="status"
          aria-label="Restoring terminal history"
          className="absolute inset-0 z-30 flex items-center justify-center bg-canvas text-ui text-muted"
        >
          Restoring terminal history
        </div>
      )}
      {image.dialog}
    </div>
  )
}

/** Covers the xterm host, which stays blank until an attach acks. */
export function TerminalSpinner({ label }: { label: string }) {
  return (
    <div
      role="status"
      aria-label={label}
      className="absolute inset-0 z-20 flex items-center justify-center gap-2 bg-canvas text-ui text-muted"
    >
      <LoaderCircle className="size-4 animate-spin motion-reduce:animate-none" aria-hidden />
      {label}
    </div>
  )
}
