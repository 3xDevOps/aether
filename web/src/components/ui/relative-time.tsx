import type { ComponentProps } from 'react'
import { useClock } from '@/lib/clock'
import { timeAgo } from '@/lib/format'

/**
 * "3 minutes ago" for an ISO instant, kept fresh by the shared 30 s clock.
 * Only this element re-renders on a tick, never the row around it.
 */
export function RelativeTime({ at, ...props }: { at: string } & Omit<ComponentProps<'time'>, 'dateTime' | 'children'>) {
  useClock()
  return (
    <time dateTime={at} {...props}>
      {timeAgo(at)}
    </time>
  )
}
