import { cva } from 'class-variance-authority'
import type * as React from 'react'
import { Tooltip } from '@/components/ui/tooltip'
import { cn, focusRing } from '@/lib/utils'

// The press scale runs on its own curve, and settles back quicker than it went in.
const controlMotion =
  '[--press-duration:var(--duration-release)] active:[--press-duration:var(--duration-press)] [transition:color_var(--duration-hover)_var(--ease-hover),background-color_var(--duration-hover)_var(--ease-hover),border-color_var(--duration-hover)_var(--ease-hover),box-shadow_var(--duration-hover)_var(--ease-hover),opacity_var(--duration-hover)_var(--ease-hover),scale_var(--press-duration)_var(--ease-press)] motion-reduce:transition-none'

const buttonVariants = cva(
  // An `aria-disabled` control stays focusable and keeps its pointer, so each
  // variant suppresses hover and pressed-state paints without removing it.
  `inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-control font-medium ${controlMotion} ${focusRing} disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50 aria-disabled:cursor-not-allowed aria-disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-3.5`,
  {
    variants: {
      variant: {
        primary: 'bg-accent text-on-accent hover:not-aria-disabled:bg-accent-hover',
        secondary: 'border border-seam bg-raised text-text shadow-button hover:not-aria-disabled:bg-hover-chrome hover:not-aria-disabled:shadow-button-hover',
        ghost: 'text-muted hover:not-aria-disabled:bg-hover-chrome hover:not-aria-disabled:text-text',
        danger: 'bg-state-failed text-on-failed hover:not-aria-disabled:bg-state-failed/90',
        link: 'relative text-accent underline-offset-2 hover:not-aria-disabled:underline coarse:after:absolute coarse:after:inset-x-0 coarse:after:top-1/2 coarse:after:h-11 coarse:after:-translate-y-1/2',
        quiet: 'relative text-muted underline-offset-2 hover:not-aria-disabled:text-text hover:not-aria-disabled:underline coarse:after:absolute coarse:after:inset-x-0 coarse:after:top-1/2 coarse:after:h-11 coarse:after:-translate-y-1/2',
      },
      size: {
        sm: 'h-6 px-2 text-ui-sm coarse:h-11 coarse:px-3',
        md: 'h-7 px-2.5 text-ui coarse:h-11 coarse:px-3',
        icon: "size-7 coarse:size-11 [&_svg:not([class*='size-'])]:size-4",
        'icon-sm': 'size-6 coarse:size-11',
        round: "size-7 rounded-full coarse:size-11 [&_svg:not([class*='size-'])]:size-4",
        pill: 'h-7 rounded-full px-3 text-ui coarse:h-11 coarse:px-4',
      },
    },
    compoundVariants: [
      { variant: ['link', 'quiet'], className: 'h-auto px-0 coarse:h-auto coarse:px-0' },
      // Only a fine pointer presses in: a finger covers the control it taps, so touch darkens it instead.
      { variant: ['primary', 'secondary', 'ghost', 'danger'], className: 'pointer-fine:active:not-aria-disabled:scale-press coarse:active:not-aria-disabled:brightness-[0.96]' },
    ],
    defaultVariants: { variant: 'primary', size: 'md' },
  },
)

type Variant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'link' | 'quiet'

export type ButtonProps = React.ComponentProps<'button'> & {
  variant?: Variant
  hint?: React.ReactNode
} & ({ size?: 'sm' | 'md' | 'pill'; label?: undefined } | { size: 'icon' | 'icon-sm' | 'round'; label: string })

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
