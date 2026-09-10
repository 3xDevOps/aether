import { cva, type VariantProps } from 'class-variance-authority'
import type * as React from 'react'
import { cn, focusRing } from '@/lib/utils'

const buttonVariants = cva(
  // An `aria-disabled` control stays focusable and keeps its pointer, so each
  // variant suppresses hover and pressed-state paints without removing it.
  `inline-flex min-h-[26px] shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-[2px] text-[13px] font-medium transition-[background-color,border-color,color,box-shadow] duration-100 motion-reduce:transition-none ${focusRing} disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50 aria-disabled:cursor-not-allowed aria-disabled:opacity-50 [&_svg]:pointer-events-none [&_svg:not([class*='size-'])]:size-4`,
  {
    variants: {
      variant: {
        default:
          'bg-primary text-primary-foreground hover:not-aria-disabled:bg-primary-hover active:not-aria-disabled:bg-primary-hover',
        secondary:
          'bg-secondary text-secondary-foreground hover:not-aria-disabled:bg-toolbar-hover active:not-aria-disabled:bg-toolbar-hover',
        outline:
          'border border-input bg-background hover:not-aria-disabled:bg-toolbar-hover hover:not-aria-disabled:text-foreground active:not-aria-disabled:bg-toolbar-hover',
        ghost:
          'hover:not-aria-disabled:bg-toolbar-hover hover:not-aria-disabled:text-foreground active:not-aria-disabled:bg-toolbar-hover',
        destructive:
          'bg-destructive text-destructive-foreground hover:not-aria-disabled:bg-destructive/90 active:not-aria-disabled:bg-destructive/90',
      },
      size: {
        default: 'h-[26px] rounded-[2px] px-2.5 py-0',
        sm: 'h-[22px] min-h-[22px] rounded-[2px] px-2 py-0 text-[12px]',
        icon: 'size-[22px] min-h-[22px] min-w-[22px] rounded-[2px] p-0',
      },
    },
    defaultVariants: { variant: 'default', size: 'default' },
  },
)

export function Button({
  className,
  variant,
  size,
  ...props
}: React.ComponentProps<'button'> & VariantProps<typeof buttonVariants>) {
  return (
    <button
      data-slot="button"
      className={cn(buttonVariants({ variant, size, className }))}
      {...props}
    />
  )
}

export { buttonVariants }
