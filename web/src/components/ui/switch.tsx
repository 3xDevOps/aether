import { Switch as SwitchPrimitive } from 'radix-ui'
import type * as React from 'react'
import { coarseHitArea } from '@/components/ui/checkbox'
import { cn, focusRing } from '@/lib/utils'

export function Switch({ className, ...props }: React.ComponentProps<typeof SwitchPrimitive.Root>) {
  return (
    <SwitchPrimitive.Root
      data-slot="switch"
      className={cn(
        focusRing,
        coarseHitArea,
        'inline-flex h-4 w-7 shrink-0 items-center rounded-full border border-control bg-canvas px-0.5 transition-colors duration-100 motion-reduce:transition-none',
        'disabled:cursor-not-allowed disabled:opacity-50 data-[state=checked]:border-accent data-[state=checked]:bg-accent',
        className,
      )}
      {...props}
    >
      <SwitchPrimitive.Thumb className="block size-2.5 rounded-full bg-icon-faint transition-[translate,background-color] duration-150 ease-pop data-[state=checked]:translate-x-3 data-[state=checked]:bg-on-accent motion-reduce:transition-none" />
    </SwitchPrimitive.Root>
  )
}
