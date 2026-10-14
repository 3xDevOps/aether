import { Popover as PopoverPrimitive } from 'radix-ui'
import type * as React from 'react'
import { ChevronDown } from '@/components/icons'
import { cn, field, surface } from '@/lib/utils'

function Popover(props: React.ComponentProps<typeof PopoverPrimitive.Root>) {
  return <PopoverPrimitive.Root data-slot="popover" {...props} />
}

function PopoverTrigger(props: React.ComponentProps<typeof PopoverPrimitive.Trigger>) {
  return <PopoverPrimitive.Trigger data-slot="popover-trigger" {...props} />
}

/** A field-shaped trigger, like SelectTrigger, for a popover that picks a value. */
function PopoverFieldTrigger({ className, children, ...props }: React.ComponentProps<'button'>) {
  return (
    <PopoverPrimitive.Trigger asChild>
      <button
        type="button"
        role="combobox"
        data-slot="popover-field-trigger"
        className={cn(field, 'flex items-center justify-between gap-2 text-left', className)}
        {...props}
      >
        {children}
        <ChevronDown aria-hidden className="size-3.5 shrink-0 text-muted" />
      </button>
    </PopoverPrimitive.Trigger>
  )
}

function PopoverAnchor(props: React.ComponentProps<typeof PopoverPrimitive.Anchor>) {
  return <PopoverPrimitive.Anchor data-slot="popover-anchor" {...props} />
}

function PopoverContent({
  className,
  sideOffset = 4,
  align = 'start',
  ...props
}: React.ComponentProps<typeof PopoverPrimitive.Content>) {
  return (
    <PopoverPrimitive.Portal>
      <PopoverPrimitive.Content
        data-slot="popover-content"
        sideOffset={sideOffset}
        align={align}
        collisionPadding={8}
        className={cn(
          surface,
          'z-50 max-h-[min(640px,calc(100dvh-16px),var(--radix-popover-content-available-height))] w-[min(420px,calc(100vw-16px))] animate-overlay-in overflow-y-auto p-3 text-ui outline-none motion-reduce:animate-none',
          className,
        )}
        {...props}
      />
    </PopoverPrimitive.Portal>
  )
}

export { Popover, PopoverAnchor, PopoverContent, PopoverFieldTrigger, PopoverTrigger }
