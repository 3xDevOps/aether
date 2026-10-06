import type { ComponentProps } from 'react'
import { useClock } from '@/lib/clock'
import { timeAgo } from '@/lib/format'

export function RelativeTime({ at, ...props }: { at: string } & Omit<ComponentProps<'time'>, 'dateTime' | 'children'>) {
  useClock()
  const when = new Date(at)
  return (
    <time dateTime={at} title={Number.isNaN(when.getTime()) ? undefined : when.toLocaleString()} {...props}>
      {timeAgo(at)}
    </time>
  )
}
