import type * as React from 'react'
import { cn } from '@/lib/utils'

/**
 * A fill across a track. With a `role` it is the meter itself and reports
 * `value` of `max`; without one it only draws, for a parent that carries the
 * role and its own value text.
 */
export function ProgressBar({
  value,
  max = 100,
  role,
  className,
  fillClassName,
  ...props
}: Omit<React.ComponentProps<'span'>, 'role'> & {
  value: number
  max?: number
  role?: 'meter' | 'progressbar'
  fillClassName?: string
}) {
  const percent = max > 0 ? Math.min(100, Math.max(0, (value / max) * 100)) : 0
  return (
    <span
      data-slot="progress-bar"
      {...(role ? { role, 'aria-valuemin': 0, 'aria-valuemax': max, 'aria-valuenow': Math.min(Math.max(value, 0), max) } : {})}
      className={cn('block h-1 overflow-hidden rounded-full bg-chrome', className)}
      {...props}
    >
      <span
        data-slot="progress-fill"
        className={cn(
          'block h-full rounded-full bg-accent transition-[width] duration-glide ease-glide motion-reduce:transition-none',
          fillClassName,
        )}
        style={{ width: `${percent}%` }}
      />
    </span>
  )
}
