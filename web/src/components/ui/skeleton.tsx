import type * as React from 'react'
import { cn } from '@/lib/utils'

export function Skeleton({ className, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="skeleton"
      className={cn(
        'relative overflow-hidden rounded-control bg-chrome',
        'after:absolute after:inset-0 after:animate-skeleton-sweep after:bg-linear-to-r after:from-transparent after:via-hover-chrome after:to-transparent motion-reduce:after:hidden',
        className,
      )}
      {...props}
    />
  )
}
