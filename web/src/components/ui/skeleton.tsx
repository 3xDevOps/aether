import type * as React from 'react'
import { cn } from '@/lib/utils'

export function Skeleton({ className, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="skeleton"
      className={cn('animate-pulse rounded-[2px] bg-muted/70 motion-reduce:animate-none', className)}
      {...props}
    />
  )
}
