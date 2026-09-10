import { cva, type VariantProps } from 'class-variance-authority'
import type * as React from 'react'
import { cn, focusRing } from '@/lib/utils'

const buttonVariants = cva(
  // A control blocked with `aria-disabled` keeps its focus and its pointer, so
  // the hover and the press each variant paints have to stand down on their
  // own rather than through `pointer-events-none`.
  `inline-flex min-h-8 shrink-0 items-center justify-center gap-2 whitespace-nowrap rounded-md text-sm font-medium transition-[background-color,border-color,color,box-shadow,transform] duration-150 ${focusRing} active:translate-y-px disabled:pointer-events-none disabled:opacity-50 aria-disabled:cursor-not-allowed aria-disabled:opacity-50 aria-disabled:active:translate-y-0 [&_svg]:pointer-events-none [&_svg:not([class*='size-'])]:size-4`,
  {
    variants: {
      variant: {
        default: 'bg-primary text-primary-foreground hover:not-aria-disabled:bg-primary/90',
        secondary:
          'bg-secondary text-secondary-foreground hover:not-aria-disabled:bg-secondary/80',
        outline:
          'border border-input bg-background hover:not-aria-disabled:bg-accent hover:not-aria-disabled:text-accent-foreground',
        ghost:
          'hover:not-aria-disabled:bg-accent hover:not-aria-disabled:text-accent-foreground',
        destructive:
          'bg-destructive text-destructive-foreground hover:not-aria-disabled:bg-destructive/90',
      },
      size: {
        default: 'h-9 rounded-md px-3.5 py-2 text-sm',
        sm: 'h-8 rounded-sm px-2.5 py-1.5 text-[13px]',
        icon: 'size-8 min-h-8 min-w-8 rounded-md',
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
