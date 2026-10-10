import type { ComponentProps } from 'react'
import { useClock } from '@/lib/clock'
import { compactAge, timeAgo } from '@/lib/format'

export function RelativeTime({ at, compact = false, ...props }: {
  at: string
  compact?: boolean
} & Omit<ComponentProps<'time'>, 'dateTime' | 'children'>) {
  useClock()
  const when = new Date(at)
  return (
    <time dateTime={at} title={Number.isNaN(when.getTime()) ? undefined : when.toLocaleString()} {...props}>
      {compact ? compactAge(at) : timeAgo(at)}
    </time>
  )
}
