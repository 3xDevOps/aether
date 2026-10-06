import { Label as LabelPrimitive } from 'radix-ui'
import type * as React from 'react'
import { cn } from '@/lib/utils'

export function Label({ className, ...props }: React.ComponentProps<typeof LabelPrimitive.Root>) {
  // Bare text is not a space-y child, so a field wrapped without a caption needs its own gap.
  return (
    <LabelPrimitive.Root
      data-slot="label"
      className={cn(
        'block text-ui font-medium text-text [&>span]:block [&>input:first-child:not([type=checkbox]):not([type=radio])]:mt-1 [&>select:first-child]:mt-1 [&>textarea:first-child]:mt-1',
        className,
      )}
      {...props}
    />
  )
}
