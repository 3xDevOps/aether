import type { ReactNode } from 'react'
import { Undo2 } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { TextSwap } from '@/components/ui/text-swap'
import type { MemoryItem } from '@/lib/types'
import { cn } from '@/lib/utils'
import { useName } from '@/routes/memory/note'

/**
 * The panel's status line. It crossfades when its message changes, keyed by
 * `swapKey`, never by a ticking value. With no message the band folds away
 * and only the live region stays.
 */
export function FooterBand({ swapKey, children }: { swapKey: string; children: ReactNode }) {
  return (
    <div
      className={cn(
        'flex h-10 shrink-0 items-center border-t border-seam px-4 text-ui-sm text-muted tabular-nums coarse:h-12 max-sm:h-auto max-sm:min-h-12 max-sm:py-2',
        !children && 'h-0 overflow-hidden border-t-0 coarse:h-0 max-sm:h-0 max-sm:min-h-0 max-sm:py-0',
      )}
    >
      <div role="status" className="min-w-0 flex-1">
        <TextSwap swapKey={swapKey} mode="crossfade" className="grid w-full" layerClassName="flex min-w-0 items-center gap-2">
          {children}
        </TextSwap>
      </div>
    </div>
  )
}

export function Line({ children }: { children: ReactNode }) {
  return <span className="min-w-0 truncate max-sm:line-clamp-2 max-sm:whitespace-normal">{children}</span>
}

export function UndoBar({ item, onUndo }: { item: MemoryItem; onUndo: () => void }) {
  return (
    <>
      <span className="flex min-w-0 flex-1">
        <span className="shrink-0 whitespace-pre">Removed “</span>
        <span className="truncate text-text">{item.title}</span>
        <span className="shrink-0">”</span>
      </span>
      <Button variant="secondary" size="sm" onClick={onUndo}>
        <Undo2 aria-hidden />
        Undo
      </Button>
    </>
  )
}

/** What a note does for new runs, for the row the pointer or focus is on. */
export function RowHint({ item }: { item: MemoryItem }) {
  const who = useName(item.pinned_by, item.pinned_by_name, 'a member')
  if (item.superseded_by) return <Line>Superseded by {item.superseded_by}</Line>
  if (item.invalid_at) return <Line>Replaced · listed under Superseded</Line>
  if (!item.pinned_at) return <Line>Not pinned · sessions find it by searching</Line>
  if (item.scope === 'member') {
    return <Line>Pinned for {who} · in {who === 'you' ? 'your' : `${who}'s`} new sessions' briefs</Line>
  }
  return <Line>Pinned by {who} · in every new session's brief</Line>
}

/** What RowHint says for a note, so the footer stays still while the pointer crosses rows that share it. */
export function hintKey(item: MemoryItem): string {
  if (item.superseded_by) return `superseded\n${item.superseded_by}`
  if (item.invalid_at) return 'replaced'
  if (!item.pinned_at) return 'unpinned'
  return `${item.scope}\n${item.pinned_by ?? ''}`
}
