import type { ReactNode, Ref } from 'react'
import { Code } from '@/components/ui/code'
import { EmptyState } from '@/components/ui/empty-state'
import { FailureCallout } from '@/components/ui/failure-callout'
import { SectionLabel } from '@/components/ui/section-label'
import { Skeleton } from '@/components/ui/skeleton'
import type { MemoryItem } from '@/lib/types'
import { cn } from '@/lib/utils'
import { filters } from '@/routes/memory/model'
import type { Load } from '@/routes/memory/use-notes'

/**
 * The list area: skeleton rows on a slow first read, the failure with Retry,
 * an empty line, or the notes, pinned first. Pinned and Newest (Best matches
 * while searching) head the two groups when both have notes, inside one list
 * so a pin moves its row without remounting it, focus included. While a new
 * filter or search is read, the old list stays, dimmed.
 */
export function NoteList({
  load,
  items,
  stale,
  skeleton,
  listRef,
  onRetry,
  row,
}: {
  load: Load
  items: MemoryItem[]
  stale: boolean
  skeleton: boolean
  listRef: Ref<HTMLDivElement>
  onRetry: () => void
  row: (item: MemoryItem) => ReactNode
}) {
  const pinned = items.filter((item) => item.pinned_at).length
  const headed = pinned > 0 && pinned < items.length
  const heading = (index: number) => {
    if (!headed || (index !== 0 && index !== pinned)) return null
    const label = index === 0 ? 'Pinned' : load.kind === 'ready' && load.words ? 'Best matches' : 'Newest'
    return <SectionLabel as="h2" className={cn('px-2.5 pb-1', index === 0 ? 'pt-2' : 'pt-4')}>{label}</SectionLabel>
  }

  return (
    <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain">
      <div
        ref={listRef}
        className={cn('px-1.5 py-1.5 transition-opacity duration-200 motion-reduce:transition-none', stale && 'opacity-50')}
      >
        {load.kind === 'loading' && skeleton && (
          <div aria-hidden className="flex flex-col gap-1.5 px-2.5 py-2">
            {[0, 1, 2].map((i) => <Skeleton key={i} className="h-12" />)}
          </div>
        )}
        {load.kind === 'failed' && <div className="p-2.5"><FailureCallout error={load.message} onRetry={onRetry} /></div>}
        {load.kind === 'ready' && items.length === 0 && (
          load.words || load.filter !== 'all'
            ? (
              <p className="flex h-16 items-center justify-center px-4 text-center text-ui text-muted">
                {filters.find((f) => f.value === load.filter)!.empty}{load.words ? <> match “{load.words}”</> : null}.
              </p>
            )
            : (
              <EmptyState title="No notes yet." className="py-10">
                Sessions add decisions, conventions and gotchas with <Code>aether-internal memory add</Code>, and each session that reports success or failure records its outcome here.
              </EmptyState>
            )
        )}
        {items.length > 0 && (
          <ul aria-label="Notes" className="flex min-w-0 flex-col">
            {items.map((item, index) => (
              <li key={item.id} className="min-w-0">
                {heading(index)}
                <div data-glide-key={item.id}>{row(item)}</div>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}
