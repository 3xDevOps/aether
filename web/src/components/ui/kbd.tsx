import type * as React from 'react'
import { cn } from '@/lib/utils'

export function Kbd({ size = 'md', className, ...props }: React.ComponentProps<'kbd'> & { size?: 'sm' | 'md' }) {
  return (
    <kbd
      data-slot="kbd"
      className={cn(
        'inline-flex shrink-0 items-center justify-center rounded-chip border border-seam font-sans text-ui-xs font-medium whitespace-nowrap text-muted',
        size === 'md' ? 'h-5 min-w-5 px-1' : 'h-4 min-w-4 px-0.75',
        className,
      )}
      {...props}
    />
  )
}
