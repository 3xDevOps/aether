import type * as React from 'react'
import { cn } from '@/lib/utils'

export function Card({
  selected = false,
  className,
  ...props
}: React.ComponentProps<'article'> & { selected?: boolean }) {
  return (
    <article
      data-slot="card"
      data-selected={selected || undefined}
      className={cn(
        'group/card relative flex min-w-0 flex-col gap-1 rounded-panel border p-3 hover:bg-hover',
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
}: {
  children: React.ReactNode
  onOpen: () => void
  label?: string
}) {
  return (
    <h3 className="min-w-0 text-ui font-medium text-text">
      <button
        type="button"
        data-card-open
        aria-label={label}
        onClick={onOpen}
        className="line-clamp-2 cursor-pointer text-left break-words outline-none after:absolute after:inset-0 after:rounded-panel focus-visible:after:outline-2 focus-visible:after:-outline-offset-1 focus-visible:after:outline-accent"
      >
        {children}
      </button>
    </h3>
  )
}

export function CardControls({ className, ...props }: React.ComponentProps<'span'>) {
  return <span data-slot="card-controls" className={cn('relative z-10 flex min-w-0 items-center gap-1.5', className)} {...props} />
}
