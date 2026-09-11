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
        'h-[26px] min-h-[26px] rounded-[2px] px-2 py-0 text-[13px] leading-6 flex items-center justify-between gap-2 text-left coarse:h-11 coarse:min-h-11',
        '[&>span]:min-w-0 [&>span]:flex-1 [&>span]:truncate data-[placeholder]:text-muted-foreground aria-[invalid=true]:border-destructive',
        className,
      )}
      {...props}
    >
      {children}
      <SelectPrimitive.Icon asChild>
        <ChevronDownIcon className="size-3.5 shrink-0 opacity-70" />
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
    <Button className="flex h-[22px] min-h-[22px] cursor-default items-center justify-center py-0 coarse:h-11 coarse:min-h-11" {...props}>
      <Icon className="size-3.5" />
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
          'relative z-50 max-h-[min(320px,calc(var(--radix-select-content-available-height,100dvh)-4px),calc(100dvh-16px))] min-w-32 overflow-hidden rounded-[4px] border bg-popover text-popover-foreground shadow-overlay outline-none',
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
        'relative flex min-h-[22px] cursor-default items-center gap-2 rounded-[2px] py-0 pr-8 pl-2 text-[13px] leading-5 select-none coarse:min-h-11 data-[highlighted]:bg-selection data-[highlighted]:text-selection-foreground data-[disabled]:pointer-events-none data-[disabled]:opacity-50',
        className,
      )}
      {...props}
    >
      <SelectPrimitive.ItemText>{children}</SelectPrimitive.ItemText>
      <SelectPrimitive.ItemIndicator className="absolute right-2 flex items-center">
        <CheckIcon className="size-3.5" />
      </SelectPrimitive.ItemIndicator>
    </SelectPrimitive.Item>
  )
}
