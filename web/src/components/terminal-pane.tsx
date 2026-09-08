// The rendered half of a terminal: the element xterm draws into, plus the
// find bar that searches its scrollback. Every terminal surface - the run
// terminal, the run-shell dock and the environment dock - renders this, so
// find behaves the same in all three.

import { ChevronDown, ChevronUp, X } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import type * as React from 'react'
import type { SearchAddon } from '@xterm/addon-search'
import type { XtermController } from '@/components/xterm-host'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

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

  // `incremental` grows the current match while the term is still being
  // typed, so the pane follows the search rather than jumping a match ahead.
  const find = (direction: 'next' | 'previous', incremental = false) => {
    if (!term) {
      setMissing(false)
      return
    }
    const found =
      direction === 'next'
        ? search?.findNext(term, { incremental })
        : search?.findPrevious(term)
    setMissing(!found)
  }

  return (
    <div className="absolute right-2 top-2 z-10 flex items-center gap-1 rounded-md border bg-card p-1 shadow-md">
      <input
        ref={input}
        aria-label="Find in terminal"
        placeholder="Find"
        value={term}
        className="w-40 rounded-md border bg-background px-2 py-1 text-sm outline-none focus-visible:ring-[2px] focus-visible:ring-ring/50"
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
          find(event.shiftKey ? 'previous' : 'next', !event.shiftKey)
        }}
      />
      {missing && <span className="px-1 text-xs text-muted-foreground">No matches</span>}
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
  /** Overlays the dock draws on top of the terminal, such as its spinner. */
  children?: React.ReactNode
}) {
  return (
    <div className="relative h-full min-h-0">
      <div
        ref={controller.hostRef}
        className={cn('h-full min-h-0 bg-background p-2 text-foreground', className)}
      />
      {controller.findOpen && (
        <FindBar search={controller.search} onClose={() => controller.setFindOpen(false)} />
      )}
      {children}
    </div>
  )
}
