import type * as React from 'react'
import { type Tone, toneClasses } from '@/components/ui/status-dot'
import { cn } from '@/lib/utils'

export function Callout({
  tone,
  title,
  actions,
  children,
  className,
  ...props
}: Omit<React.ComponentProps<'div'>, 'title'> & {
  tone: Tone
  title?: React.ReactNode
  actions?: React.ReactNode
}) {
  const { text, soft } = toneClasses(tone)
  return (
    <div
      data-slot="callout"
      data-tone={tone}
      className={cn('flex flex-col gap-1 rounded-panel px-3 py-2 text-ui text-text', soft, className)}
      {...props}
    >
      {title && <p className={cn('font-medium', text)}>{title}</p>}
      {children && <div className="min-w-0 break-words">{children}</div>}
      {actions && <div className="mt-1 flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  )
}
