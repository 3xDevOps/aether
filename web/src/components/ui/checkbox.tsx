import { Checkbox as CheckboxPrimitive } from 'radix-ui'
import type * as React from 'react'
import { Check } from '@/components/icons'
import { cn, focusRing } from '@/lib/utils'

export const coarseHitArea = "relative coarse:after:absolute coarse:after:-inset-3.5 coarse:after:content-['']"

export function Checkbox({ className, ...props }: React.ComponentProps<typeof CheckboxPrimitive.Root>) {
  return (
    <CheckboxPrimitive.Root
      data-slot="checkbox"
      className={cn(
        focusRing,
        coarseHitArea,
        'size-4 shrink-0 rounded-control border border-control bg-canvas text-on-accent transition-colors duration-100 motion-reduce:transition-none',
        'disabled:cursor-not-allowed disabled:opacity-50 data-[state=checked]:border-accent data-[state=checked]:bg-accent',
        className,
      )}
      {...props}
    >
      <CheckboxPrimitive.Indicator className="flex items-center justify-center">
        <Check className="size-3" strokeWidth={2.5} />
      </CheckboxPrimitive.Indicator>
    </CheckboxPrimitive.Root>
  )
}
