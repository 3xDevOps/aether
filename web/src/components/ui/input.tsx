import type * as React from 'react'
import { cn, field } from '@/lib/utils'

export function Input({ className, ...props }: React.ComponentProps<'input'>) {
  return (
    <input
      data-slot="input"
      className={cn(
        field,
        'h-[26px] min-h-[26px] rounded-[2px] px-2 py-0 text-[13px] leading-6 transition-[background-color,border-color,color,box-shadow] duration-100 motion-reduce:transition-none placeholder:text-muted-foreground',
        'read-only:bg-muted/30 read-only:text-muted-foreground aria-[invalid=true]:border-destructive',
        className,
      )}
      {...props}
    />
  )
}
