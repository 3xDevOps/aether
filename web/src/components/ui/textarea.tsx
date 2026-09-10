import type * as React from 'react'
import { cn, field } from '@/lib/utils'

export function Textarea({ className, ...props }: React.ComponentProps<'textarea'>) {
  return (
    <textarea
      data-slot="textarea"
      className={cn(
        field,
        'h-auto min-h-16 resize-y rounded-[2px] px-2 py-1 text-[13px] leading-5 transition-[background-color,border-color,color,box-shadow] duration-100 motion-reduce:transition-none placeholder:text-muted-foreground',
        'read-only:bg-muted/30 read-only:text-muted-foreground aria-[invalid=true]:border-destructive',
        className,
      )}
      {...props}
    />
  )
}
