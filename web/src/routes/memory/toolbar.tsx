import { type Ref, useEffect, useRef } from 'react'
import { Search, X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { FadeStrip } from '@/components/ui/fade-strip'
import { Input } from '@/components/ui/input'
import { RadioPills, RadioPillsItem } from '@/components/ui/radio-pills'
import { cn } from '@/lib/utils'
import { filters, type Filter } from '@/routes/memory/model'

const swap = 'transition-opacity duration-250 ease-standard motion-reduce:transition-none'

/**
 * The filter pills with their counts, and Search, which takes the row while
 * it is open. The hidden half is inert, so only one control answers.
 */
export function ShowBar({
  filter,
  onFilter,
  counts,
  searching,
  typed,
  onTyped,
  onOpenSearch,
  onCloseSearch,
  searchRef,
}: {
  filter: Filter
  onFilter: (filter: Filter) => void
  counts: Partial<Record<Filter, string>>
  searching: boolean
  typed: string
  onTyped: (typed: string) => void
  onOpenSearch: () => void
  onCloseSearch: () => void
  searchRef: Ref<HTMLButtonElement>
}) {
  const pills = useRef<HTMLDivElement>(null)
  const field = useRef<HTMLInputElement>(null)

  useEffect(() => {
    pills.current?.querySelector('[data-state=checked]')?.scrollIntoView?.({ inline: 'nearest', block: 'nearest' })
  }, [filter])

  useEffect(() => {
    if (searching) field.current?.focus()
  }, [searching])

  return (
    <div className="relative flex h-11 shrink-0 items-center border-b border-seam coarse:h-14">
      <div inert={searching} aria-hidden={searching || undefined} className={cn('flex size-full min-w-0 items-center gap-1 pr-2 pl-4', swap, searching && 'opacity-0')}>
        <FadeStrip>
          <RadioPills ref={pills} aria-label="Show" value={filter} onValueChange={(value) => onFilter(value as Filter)}>
            {filters.map((f) => (
              <RadioPillsItem key={f.value} value={f.value}>
                {f.label}
                {counts[f.value] && ' '}
                {counts[f.value] && <span className="text-ui-sm font-normal text-muted tabular-nums">{counts[f.value]}</span>}
              </RadioPillsItem>
            ))}
          </RadioPills>
        </FadeStrip>
        <Button ref={searchRef} variant="ghost" size="icon" label="Search notes" onClick={onOpenSearch}>
          <Search />
        </Button>
      </div>
      <div inert={!searching} aria-hidden={!searching || undefined} className={cn('absolute inset-y-0 right-2 left-4 flex items-center', swap, !searching && 'opacity-0')}>
        <Search aria-hidden className="pointer-events-none absolute left-2 size-3.5 text-muted" />
        <Input
          ref={field}
          type="search"
          aria-label="Search notes"
          placeholder={filters.find((f) => f.value === filter)!.search}
          className="pr-8 pl-7 [&::-webkit-search-cancel-button]:appearance-none"
          value={typed}
          onChange={(e) => onTyped(e.target.value)}
          onKeyDown={(e) => {
            if (e.key !== 'Escape') return
            e.preventDefault()
            e.stopPropagation()
            onCloseSearch()
          }}
        />
        <Button variant="ghost" size="icon-sm" label="Close search" className="absolute right-0.5" onClick={onCloseSearch}>
          <X />
        </Button>
      </div>
    </div>
  )
}
