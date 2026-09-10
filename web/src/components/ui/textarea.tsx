import type * as React from 'react'
import { cn, field } from '@/lib/utils'

export function Textarea({ className, ...props }: React.ComponentProps<'textarea'>) {
  return (
    <textarea
      data-slot="textarea"
      className={cn(
        field,
        'h-auto min-h-20 resize-y transition-[background-color,border-color,color,box-shadow] placeholder:text-muted-foreground disabled:cursor-not-allowed disabled:opacity-50',
        className,
      )}
      {...props}
    />
  )
}
