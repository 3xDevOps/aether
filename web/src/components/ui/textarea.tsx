import type * as React from 'react'
import { cn, field } from '@/lib/utils'

export function Textarea({ variant = 'default', className, ...props }: React.ComponentProps<'textarea'> & { variant?: 'default' | 'embedded' }) {
  return (
    <textarea
      data-slot="textarea"
      className={cn(
        field,
        'h-auto min-h-16 resize-y py-1 transition-colors duration-100 motion-reduce:transition-none coarse:h-auto',
        variant === 'embedded' && 'rounded-none border-0 focus-visible:outline-none focus-visible:shadow-none',
        className,
      )}
      {...props}
    />
  )
}
