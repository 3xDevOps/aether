// The rendered half of a terminal: the element xterm draws into, plus the
// find bar that searches its scrollback. Every terminal surface - the run
// terminal, the run-shell dock and the environment dock - renders this, so
// find behaves the same in all three.

import { ChevronDown, ChevronUp, Loader2, X } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import type * as React from 'react'
import type { SearchAddon } from '@xterm/addon-search'
import type { XtermController } from '@/components/xterm-host'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
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

  const find = (direction: 'next' | 'previous') => {
    if (!search || !term) {
      setMissing(false)
      return
    }
    const found = direction === 'next' ? search.findNext(term) : search.findPrevious(term)
    setMissing(!found)
  }

  return (
    <div className="absolute right-2 top-2 z-10 flex items-center gap-1 rounded-md border bg-card p-1 shadow-md">
      <Input
        ref={input}
        aria-label="Find in terminal"
        placeholder="Find"
        value={term}
        className="w-40"
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
        <span role="status" className="px-1 text-xs text-muted-foreground">
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
  /** Overlays drawn on top of the terminal, such as `TerminalSpinner`. */
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
