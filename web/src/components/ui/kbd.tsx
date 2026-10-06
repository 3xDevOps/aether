import type * as React from 'react'
import { cn } from '@/lib/utils'

export function Kbd({ className, ...props }: React.ComponentProps<'kbd'>) {
  return (
    <kbd
      data-slot="kbd"
      className={cn(
        'inline-flex h-5 min-w-5 items-center justify-center rounded-control border border-seam bg-chrome px-1 font-code text-ui-xs text-muted',
        className,
      )}
      {...props}
    />
  )
}
