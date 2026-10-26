import type * as React from 'react'
import { cn } from '@/lib/utils'

/** A count beside a label. It changes at once, so a ticking count never reflows its row. */
export function CountBadge({ className, ...props }: React.ComponentProps<'span'>) {
  return (
    <span
      data-slot="count-badge"
      className={cn(
        'inline-flex h-4.5 min-w-4.5 shrink-0 items-center justify-center rounded-chip bg-seam px-1 text-ui-xs font-medium whitespace-nowrap text-muted tabular-nums',
        className,
      )}
      {...props}
    />
  )
}
