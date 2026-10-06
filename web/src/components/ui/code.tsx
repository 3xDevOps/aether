import type * as React from 'react'
import { cn } from '@/lib/utils'

export function Code({ className, ...props }: React.ComponentProps<'code'>) {
  return (
    <code
      data-slot="code"
      className={cn('box-decoration-clone rounded-control bg-chrome px-1 font-code text-ui-sm break-words text-text', className)}
      {...props}
    />
  )
}

export function CodeBlock({ className, ...props }: React.ComponentProps<'pre'>) {
  return (
    <pre
      data-slot="code-block"
      className={cn(
        'overflow-x-auto rounded-control border border-seam bg-chrome p-3 font-code text-ui-sm whitespace-pre text-text',
        className,
      )}
      {...props}
    />
  )
}
