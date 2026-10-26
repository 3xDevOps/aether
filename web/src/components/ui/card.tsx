import type * as React from 'react'
import { cn } from '@/lib/utils'

/** `raised` is for a card on a `bg-chrome` well, such as a Board column. */
export function Card({
  selected = false,
  raised = false,
  className,
  ...props
}: React.ComponentProps<'article'> & { selected?: boolean; raised?: boolean }) {
  return (
    <article
      data-slot="card"
      data-selected={selected || undefined}
      className={cn(
        'group/card relative flex min-w-0 flex-col gap-1 rounded-panel border p-3 shadow-card transition-[background-color,border-color,box-shadow] motion-reduce:transition-none hover:shadow-card-hover',
        raised ? 'bg-raised' : 'hover:bg-hover',
        selected ? 'border-accent' : 'border-seam',
        className,
      )}
      {...props}
    />
  )
}

// The button's overlay spans the card, so a click anywhere opens it; controls
// inside the card sit above the overlay with `CardControls`.
export function CardTitle({
  children,
  onOpen,
  label,
  className,
}: {
  children: React.ReactNode
  onOpen: () => void
  label?: string
  className?: string
}) {
  return (
    <h3 className={cn('min-w-0 text-ui font-medium text-text', className)}>
      <button
        type="button"
        data-card-open
        aria-label={label}
        onClick={onOpen}
        className={cn('line-clamp-2 cursor-pointer text-left break-words outline-none after:absolute after:inset-0 after:rounded-panel focus-visible:after:outline-2 focus-visible:after:-outline-offset-1 focus-visible:after:outline-accent', className)}
      >
        {children}
      </button>
    </h3>
  )
}

export function CardControls({ className, ...props }: React.ComponentProps<'span'>) {
  return <span data-slot="card-controls" className={cn('relative z-10 flex min-w-0 items-center gap-1.5', className)} {...props} />
}
