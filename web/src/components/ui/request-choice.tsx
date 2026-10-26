import { RadioGroup as RadioGroupPrimitive } from 'radix-ui'
import type * as React from 'react'
import { Button } from '@/components/ui/button'
import { Kbd } from '@/components/ui/kbd'
import { TextSwap } from '@/components/ui/text-swap'
import { cn, focusRing } from '@/lib/utils'

export function RequestFooter({ className, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="request-footer"
      className={cn('sticky bottom-0 flex min-w-0 flex-col gap-2 border-t border-seam bg-chrome px-4 pt-3 pb-3.5', className)}
      {...props}
    />
  )
}

export function RequestActions({ extras, className, children }: { extras?: React.ReactNode; className?: string; children: React.ReactNode }) {
  return (
    <div className={cn('flex min-w-0 flex-wrap items-center gap-2', className)}>
      {extras}
      <div className="ml-auto flex min-w-0 flex-1 flex-wrap justify-end gap-2 @min-[21rem]:flex-none [&>*]:flex-1 @min-[21rem]:[&>*]:flex-none">
        {children}
      </div>
    </div>
  )
}

function Digit({ digit, onFill }: { digit: number; onFill: boolean }) {
  return (
    <Kbd size="sm" aria-hidden className={cn('coarse:hidden', onFill && 'border-current/35 text-current')}>
      {digit}
    </Kbd>
  )
}

export function RequestAction({ label, digit, variant = 'secondary', className, ...props }: Omit<React.ComponentProps<'button'>, 'children'> & {
  label: string
  digit?: number
  variant?: 'primary' | 'secondary' | 'danger' | 'quiet'
}) {
  const filled = variant === 'primary' || variant === 'danger'
  return (
    <Button
      size="md"
      variant={variant}
      aria-keyshortcuts={digit ? String(digit) : undefined}
      className={cn('gap-2', className)}
      {...props}
    >
      <TextSwap mode="crossfade" layerClassName="whitespace-nowrap">{label}</TextSwap>
      {digit !== undefined && <Digit digit={digit} onFill={filled} />}
    </Button>
  )
}

const ignoredKeys = ['Home', 'End', 'PageUp', 'PageDown']

export function RequestPills({ value, onValueChange, items, disabled }: {
  value: string
  onValueChange: (value: string) => void
  items: { value: string; label: string }[]
  disabled?: boolean
}) {
  return (
    <RadioGroupPrimitive.Root
      data-slot="request-pills"
      aria-label="How long"
      value={value}
      onValueChange={onValueChange}
      disabled={disabled}
      orientation="horizontal"
      className="flex min-w-0 gap-2"
    >
      {items.map((item) => (
        <RadioGroupPrimitive.Item
          key={item.value}
          value={item.value}
          onKeyDown={(event) => {
            if (ignoredKeys.includes(event.key)) event.preventDefault()
          }}
          className={cn(
            focusRing,
            'flex h-6.5 min-w-0 flex-1 cursor-pointer items-center justify-center rounded-control bg-hover-chrome px-2 text-ui-sm font-medium text-muted coarse:h-8.5',
            'transition-[color,background-color,box-shadow] duration-hover ease-hover motion-reduce:transition-none',
            'hover:text-text disabled:cursor-not-allowed disabled:opacity-50',
            'data-[state=checked]:bg-raised data-[state=checked]:text-text data-[state=checked]:shadow-button data-[state=checked]:ring-1 data-[state=checked]:ring-seam data-[state=checked]:ring-inset',
          )}
        >
          {item.label}
        </RadioGroupPrimitive.Item>
      ))}
    </RadioGroupPrimitive.Root>
  )
}
