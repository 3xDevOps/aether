import { Label as LabelPrimitive } from 'radix-ui'
import type * as React from 'react'
import { cn } from '@/lib/utils'

export function Label({
  className,
  ...props
}: React.ComponentProps<typeof LabelPrimitive.Root>) {
  // Bare text is not a space-y child. Wrapped fields need their own gap only
  // when there is no caption element; block captions use the caller's spacing.
  return (
    <LabelPrimitive.Root
      data-slot="label"
      className={cn(
        'block text-[13px] font-medium leading-4 text-foreground [&>span]:block [&>input:first-child:not([type=checkbox]):not([type=radio])]:mt-1 [&>select:first-child]:mt-1 [&>textarea:first-child]:mt-1',
        className,
      )}
      {...props}
    />
  )
}
