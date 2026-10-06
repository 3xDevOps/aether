import { Separator as SeparatorPrimitive } from 'radix-ui'
import type * as React from 'react'
import { cn } from '@/lib/utils'

export function Separator({
  orientation = 'horizontal',
  decorative = true,
  className,
  ...props
}: React.ComponentProps<typeof SeparatorPrimitive.Root>) {
  return (
    <SeparatorPrimitive.Root
      data-slot="separator"
      orientation={orientation}
      decorative={decorative}
      className={cn('shrink-0 bg-seam', orientation === 'horizontal' ? 'h-px w-full' : 'h-full w-px', className)}
      {...props}
    />
  )
}
