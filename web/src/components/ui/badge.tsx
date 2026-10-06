import type * as React from 'react'
import { type Tone, toneClasses } from '@/components/ui/status-dot'
import { cn } from '@/lib/utils'

export function Badge({
  tone = 'neutral',
  className,
  ...props
}: React.ComponentProps<'span'> & { tone?: Tone }) {
  const { text, soft } = toneClasses(tone)
  return (
    <span
      data-slot="badge"
      data-tone={tone}
      className={cn(
        'inline-flex h-5 max-w-full shrink-0 items-center gap-1 rounded-control px-1.5 text-ui-sm font-medium whitespace-nowrap tabular-nums [&_svg]:size-3 [&_svg]:shrink-0',
        text,
        soft,
        className,
      )}
      {...props}
    />
  )
}
