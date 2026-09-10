import { CheckIcon, ChevronDownIcon, ChevronUpIcon } from 'lucide-react'
import { Select as SelectPrimitive } from 'radix-ui'
import type * as React from 'react'
import { cn, field, focusRing } from '@/lib/utils'

export function Select({
  onValueChange,
  ...props
}: React.ComponentProps<typeof SelectPrimitive.Root>) {
  return (
    <SelectPrimitive.Root
      data-slot="select"
      // Inside a form Radix mirrors the value into a hidden native select and
      // reports every change back through it. When a value and the option
      // carrying it arrive in the same render - a roster landing and its first
      // entry being chosen - the mirror has no such option yet, so it reports
      // the empty string and wipes what was just chosen. No item may carry an
      // empty value, so an empty report is never a real choice.
      onValueChange={(value) => {
        if (value) onValueChange?.(value)
      }}
      {...props}
    />
  )
}

export function SelectValue({
  ...props
}: React.ComponentProps<typeof SelectPrimitive.Value>) {
  return <SelectPrimitive.Value data-slot="select-value" {...props} />
}

export function SelectTrigger({
  className,
  children,
  ...props
}: React.ComponentProps<typeof SelectPrimitive.Trigger>) {
  return (
    <SelectPrimitive.Trigger
      data-slot="select-trigger"
      className={cn(
        field,
        'flex items-center justify-between gap-2 text-left',
        'data-[placeholder]:text-muted-foreground',
        '[&>span]:truncate [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg]:size-4',
        className,
      )}
      {...props}
    >
      {children}
      <SelectPrimitive.Icon asChild>
        <ChevronDownIcon className="opacity-50" />
      </SelectPrimitive.Icon>
    </SelectPrimitive.Trigger>
  )
}

/** The scroll affordances the viewport needs once a list outruns the popup.
 * Radix hides the viewport's own scrollbar, so these two chevrons are the only
 * sign a list has more in it. Not exported: a caller never places them. */
function SelectScrollButton({
  direction,
  ...props
}: { direction: 'up' | 'down' } & React.ComponentProps<
  typeof SelectPrimitive.ScrollUpButton
>) {
  const Button =
    direction === 'up'
      ? SelectPrimitive.ScrollUpButton
      : SelectPrimitive.ScrollDownButton
  const Icon = direction === 'up' ? ChevronUpIcon : ChevronDownIcon
  return (
    <Button className="flex cursor-default items-center justify-center py-1" {...props}>
      <Icon className="size-4" />
    </Button>
  )
}

export function SelectContent({
  className,
  children,
  ...props
}: React.ComponentProps<typeof SelectPrimitive.Content>) {
  return (
    <SelectPrimitive.Portal>
      <SelectPrimitive.Content
        data-slot="select-content"
        position="popper"
        className={cn(
          'relative z-50 max-h-64 min-w-32 overflow-hidden rounded-md border bg-popover text-popover-foreground shadow-lg outline-none',
          'data-[side=bottom]:translate-y-1 data-[side=top]:-translate-y-1',
          className,
        )}
        {...props}
      >
        <SelectScrollButton direction="up" />
        <SelectPrimitive.Viewport className="w-full min-w-[var(--radix-select-trigger-width)] p-1">
          {children}
        </SelectPrimitive.Viewport>
        <SelectScrollButton direction="down" />
      </SelectPrimitive.Content>
    </SelectPrimitive.Portal>
  )
}

export function SelectItem({
  className,
  children,
  ...props
}: React.ComponentProps<typeof SelectPrimitive.Item>) {
  return (
    <SelectPrimitive.Item
      data-slot="select-item"
      className={cn(
        focusRing,
        // The item sits flush against its neighbours, so the outline is drawn
        // inside it, as a menu item's is.
        'focus-visible:-outline-offset-2',
        'relative flex cursor-default items-center gap-2 rounded-sm py-1.5 pr-8 pl-2 text-sm select-none focus:bg-accent focus:text-accent-foreground data-[disabled]:pointer-events-none data-[disabled]:opacity-50',
        className,
      )}
      {...props}
    >
      <SelectPrimitive.ItemText>{children}</SelectPrimitive.ItemText>
      <SelectPrimitive.ItemIndicator className="absolute right-2 flex items-center">
        <CheckIcon className="size-4" />
      </SelectPrimitive.ItemIndicator>
    </SelectPrimitive.Item>
  )
}
