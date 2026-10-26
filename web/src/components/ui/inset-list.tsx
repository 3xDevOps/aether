import type * as React from 'react'
import { cn, focusRingInset } from '@/lib/utils'

export function InsetList({ className, ...props }: React.ComponentProps<'ul'>) {
  return (
    <ul
      data-slot="inset-list"
      className={cn('flex min-w-0 flex-col divide-y divide-seam overflow-hidden rounded-panel border border-seam', className)}
      {...props}
    />
  )
}

const row = 'flex min-h-11 min-w-0 items-center gap-3 px-3 py-2.5'

export function InsetListItem({
  onOpen,
  className,
  children,
  ...props
}: React.ComponentProps<'li'> & { onOpen?: () => void }) {
  if (!onOpen) {
    return (
      <li data-slot="inset-list-item" className={cn(row, className)} {...props}>
        {children}
      </li>
    )
  }
  return (
    <li data-slot="inset-list-item" className="transition-colors hover:bg-hover motion-reduce:transition-none" {...props}>
      <button type="button" onClick={onOpen} className={cn(row, focusRingInset, 'w-full text-left', className)}>
        {children}
      </button>
    </li>
  )
}
