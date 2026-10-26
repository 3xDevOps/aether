import type * as React from 'react'
import { cn } from '@/lib/utils'

/**
 * A fill across a track. With a `role` it is the meter itself and reports
 * `value` of `max`; without one it only draws, for a parent that carries the
 * role and its own value text. Without a `value` the fill sweeps the track:
 * work with no known progress.
 */
export function ProgressBar({
  value,
  max = 100,
  role,
  className,
  fillClassName,
  ...props
}: Omit<React.ComponentProps<'span'>, 'role'> & {
  value?: number
  max?: number
  role?: 'meter' | 'progressbar'
  fillClassName?: string
}) {
  const percent = value !== undefined && max > 0 ? Math.min(100, Math.max(0, (value / max) * 100)) : 0
  return (
    <span
      data-slot="progress-bar"
      {...(role
        ? { role, 'aria-valuemin': 0, 'aria-valuemax': max, ...(value !== undefined ? { 'aria-valuenow': Math.min(Math.max(value, 0), max) } : {}) }
        : {})}
      className={cn('block h-1 overflow-hidden rounded-full bg-chrome', className)}
      {...props}
    >
      {value === undefined ? (
        <span
          data-slot="progress-fill"
          className={cn('block h-full w-1/3 animate-progress-sweep rounded-full bg-accent motion-reduce:w-full motion-reduce:animate-none motion-reduce:opacity-muted', fillClassName)}
        />
      ) : (
        <span
          data-slot="progress-fill"
          className={cn(
            'block h-full rounded-full bg-accent transition-[width] duration-glide ease-glide motion-reduce:transition-none',
            fillClassName,
          )}
          style={{ width: `${percent}%` }}
        />
      )}
    </span>
  )
}
