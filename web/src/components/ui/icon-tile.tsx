import type * as React from 'react'
import { cn } from '@/lib/utils'

export function IconTile({ className, ...props }: React.ComponentProps<'span'>) {
  return (
    <span
      data-slot="icon-tile"
      className={cn('grid size-7 shrink-0 place-items-center rounded-control border border-seam bg-chrome text-muted', className)}
      {...props}
    />
  )
}
