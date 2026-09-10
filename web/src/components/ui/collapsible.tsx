import { ChevronRightIcon } from 'lucide-react'
import { Collapsible as CollapsiblePrimitive } from 'radix-ui'
import type * as React from 'react'
import { cn, focusRing } from '@/lib/utils'

export function Collapsible({
  ...props
}: React.ComponentProps<typeof CollapsiblePrimitive.Root>) {
  return <CollapsiblePrimitive.Root data-slot="collapsible" {...props} />
}

/** Carries the marker a native disclosure used to draw for itself: without
 * one, the caption reads as a heading nobody thinks to click. */
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
        'flex min-h-[22px] w-full cursor-pointer items-center gap-1 px-1 text-left text-[13px] leading-5 hover:bg-toolbar-hover [&[data-state=open]>svg]:rotate-90',
        className,
      )}
      {...props}
    >
      <ChevronRightIcon className="size-3 shrink-0" />
      {children}
    </CollapsiblePrimitive.Trigger>
  )
}

export function CollapsibleContent({
  ...props
}: React.ComponentProps<typeof CollapsiblePrimitive.Content>) {
  return <CollapsiblePrimitive.Content data-slot="collapsible-content" {...props} />
}
