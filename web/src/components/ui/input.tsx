import type * as React from 'react'
import { cn, field } from '@/lib/utils'

export function Input({ className, ...props }: React.ComponentProps<'input'>) {
  return (
    <input
      data-slot="input"
      className={cn(
        field,
        'transition-[background-color,border-color,color,box-shadow] placeholder:text-muted-foreground',
        className,
      )}
      {...props}
    />
  )
}
