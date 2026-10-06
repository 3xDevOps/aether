import { cva } from 'class-variance-authority'
import type * as React from 'react'
import { Tooltip } from '@/components/ui/tooltip'
import { cn, focusRing } from '@/lib/utils'

const buttonVariants = cva(
  // An `aria-disabled` control stays focusable and keeps its pointer, so each
  // variant suppresses hover and pressed-state paints without removing it.
  `inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-control font-medium transition-colors duration-100 motion-reduce:transition-none ${focusRing} active:not-aria-disabled:brightness-[0.96] disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50 aria-disabled:cursor-not-allowed aria-disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-3.5`,
  {
    variants: {
      variant: {
        primary: 'bg-accent text-on-accent hover:not-aria-disabled:bg-accent-hover',
        secondary: 'border border-seam bg-raised text-text hover:not-aria-disabled:bg-hover-chrome',
        ghost: 'text-muted hover:not-aria-disabled:bg-hover-chrome hover:not-aria-disabled:text-text',
        danger: 'bg-state-failed text-on-failed hover:not-aria-disabled:bg-state-failed/90',
        link: 'relative text-accent underline-offset-2 hover:not-aria-disabled:underline coarse:after:absolute coarse:after:inset-x-0 coarse:after:top-1/2 coarse:after:h-11 coarse:after:-translate-y-1/2',
      },
      size: {
        sm: 'h-6 px-2 text-ui-sm coarse:h-11 coarse:px-3',
        md: 'h-7 px-2.5 text-ui coarse:h-11 coarse:px-3',
        icon: "size-7 coarse:size-11 [&_svg:not([class*='size-'])]:size-4",
        'icon-sm': 'size-6 coarse:size-11',
      },
    },
    compoundVariants: [{ variant: 'link', className: 'h-auto px-0 coarse:h-auto coarse:px-0' }],
    defaultVariants: { variant: 'primary', size: 'md' },
  },
)

type Variant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'link'

export type ButtonProps = React.ComponentProps<'button'> & {
  variant?: Variant
  hint?: React.ReactNode
} & ({ size?: 'sm' | 'md'; label?: undefined } | { size: 'icon' | 'icon-sm'; label: string })

export function Button({ className, variant, size, label, hint, ...props }: ButtonProps) {
  const button = (
    <button
      data-slot="button"
      aria-label={label}
      className={cn(buttonVariants({ variant, size }), className)}
      {...props}
    />
  )
  const tip = hint ?? label
  return tip ? <Tooltip content={tip}>{button}</Tooltip> : button
}

export { buttonVariants }
