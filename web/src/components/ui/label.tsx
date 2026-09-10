import { Label as LabelPrimitive } from 'radix-ui'
import type * as React from 'react'
import { cn } from '@/lib/utils'

export function Label({
  className,
  ...props
}: React.ComponentProps<typeof LabelPrimitive.Root>) {
  return (
    <LabelPrimitive.Root
      data-slot="label"
      className={cn('text-[13px] font-medium leading-4 text-foreground', className)}
      {...props}
    />
  )
}
