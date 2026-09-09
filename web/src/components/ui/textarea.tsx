import type * as React from 'react'
import { cn, field } from '@/lib/utils'

export function Textarea({ className, ...props }: React.ComponentProps<'textarea'>) {
  return (
    <textarea
      data-slot="textarea"
      className={cn(
        field,
        'placeholder:text-muted-foreground',
        className,
      )}
      {...props}
    />
  )
}
