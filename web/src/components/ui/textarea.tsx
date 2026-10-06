import type * as React from 'react'
import { cn, field } from '@/lib/utils'

export function Textarea({ className, ...props }: React.ComponentProps<'textarea'>) {
  return (
    <textarea
      data-slot="textarea"
      className={cn(
        field,
        'h-auto min-h-16 resize-y py-1 transition-colors duration-100 motion-reduce:transition-none coarse:h-auto',
        className,
      )}
      {...props}
    />
  )
}
