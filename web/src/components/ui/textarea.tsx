import type * as React from 'react'
import { forwardRef } from 'react'
import { cn, field } from '@/lib/utils'

export const Textarea = forwardRef<HTMLTextAreaElement, React.ComponentProps<'textarea'> & { variant?: 'default' | 'embedded' }>(function Textarea({ variant = 'default', className, ...props }, ref) {
  return (
    <textarea
      data-slot="textarea"
      className={cn(
        field,
        'h-auto min-h-16 resize-y py-1 transition-colors duration-100 motion-reduce:transition-none coarse:h-auto',
        variant === 'embedded' && 'rounded-none border-0 bg-transparent px-4 pt-3 pb-1 read-only:bg-transparent focus-visible:outline-none focus-visible:shadow-none',
        className,
      )}
      {...props}
      ref={ref}
    />
  )
})
