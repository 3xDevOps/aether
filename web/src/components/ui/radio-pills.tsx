import { RadioGroup as RadioGroupPrimitive } from 'radix-ui'
import type * as React from 'react'
import { SlidingIndicator } from '@/components/ui/sliding-indicator'
import { cn, focusRingInset } from '@/lib/utils'

/**
 * A single choice shown as a row of pills, one tab stop: arrows move and
 * select, Home and End select the first and last. The checked pill sits on
 * one `SlidingIndicator` that glides to the next choice.
 */
export function RadioPills({
  value,
  className,
  children,
  ...props
}: React.ComponentProps<typeof RadioGroupPrimitive.Root> & { value: string }) {
  return (
    <RadioGroupPrimitive.Root
      data-slot="radio-pills"
      orientation="horizontal"
      value={value}
      className={cn('relative flex w-max items-center gap-1', className)}
      {...props}
    >
      <SlidingIndicator activeKey={value} className="rounded-control bg-hover-chrome ring-1 ring-seam ring-inset" />
      {children}
    </RadioGroupPrimitive.Root>
  )
}

export function RadioPillsItem({ className, value, onKeyDown, ...props }: React.ComponentProps<typeof RadioGroupPrimitive.Item>) {
  return (
    <RadioGroupPrimitive.Item
      data-slot="radio-pills-item"
      data-indicator-key={value}
      value={value}
      onKeyDown={(event) => {
        onKeyDown?.(event)
        if (event.defaultPrevented || (event.key !== 'Home' && event.key !== 'End')) return
        event.preventDefault()
        const items = [...(event.currentTarget.closest('[data-slot=radio-pills]')?.querySelectorAll<HTMLButtonElement>('[role=radio]:not([disabled])') ?? [])]
        const target = event.key === 'Home' ? items[0] : items[items.length - 1]
        target?.focus()
        target?.click()
      }}
      className={cn(
        focusRingInset,
        'relative inline-flex h-7 shrink-0 cursor-pointer items-center gap-1.5 rounded-control border border-seam px-2.5 text-ui font-medium whitespace-nowrap text-muted coarse:h-11 coarse:px-3',
        'transition-colors duration-100 motion-reduce:transition-none hover:data-[state=unchecked]:bg-hover hover:data-[state=unchecked]:text-text',
        'data-[state=checked]:border-transparent data-[state=checked]:text-text',
        className,
      )}
      {...props}
    />
  )
}
