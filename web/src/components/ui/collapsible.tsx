import { Collapsible as CollapsiblePrimitive } from 'radix-ui'
import type * as React from 'react'
import { ChevronRight } from '@/components/icons'
import { cn, focusRing } from '@/lib/utils'

export function Collapsible({
  ...props
}: React.ComponentProps<typeof CollapsiblePrimitive.Root>) {
  return <CollapsiblePrimitive.Root data-slot="collapsible" {...props} />
}

/** Draws its own disclosure marker: without one the caption reads as a heading, not a control. */
export function CollapsibleTrigger({
  className,
  children,
  ...props
}: React.ComponentProps<typeof CollapsiblePrimitive.Trigger>) {
  return (
    <CollapsiblePrimitive.Trigger
      data-slot="collapsible-trigger"
      className={cn(
        focusRing,
        'flex min-h-7 w-full cursor-pointer items-center gap-1 rounded-control px-1 text-left text-ui hover:bg-hover-chrome coarse:min-h-11 [&[data-state=open]>svg]:rotate-90',
        className,
      )}
      {...props}
    >
      <ChevronRight className="size-3 shrink-0 text-muted" />
      {children}
    </CollapsiblePrimitive.Trigger>
  )
}

export function CollapsibleContent({
  ...props
}: React.ComponentProps<typeof CollapsiblePrimitive.Content>) {
  return <CollapsiblePrimitive.Content data-slot="collapsible-content" {...props} />
}
