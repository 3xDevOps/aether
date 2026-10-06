import type * as React from 'react'
import { StatusDot } from '@/components/ui/status-dot'
import { cn } from '@/lib/utils'

export function RequestCard({
  title,
  meta,
  actions,
  children,
  className,
  ...props
}: Omit<React.ComponentProps<'div'>, 'title'> & {
  title: React.ReactNode
  meta?: React.ReactNode
  actions?: React.ReactNode
}) {
  return (
    <div
      data-slot="request-card"
      tabIndex={-1}
      className={cn('flex flex-col gap-1.5 rounded-panel bg-state-needs-you-soft px-3 py-2 text-ui text-text outline-none focus-visible:outline-2 focus-visible:outline-ring', className)}
      {...props}
    >
      <div className="flex min-w-0 items-center gap-1.5">
        <StatusDot tone="needs-you" />
        <p className="min-w-0 flex-1 font-medium break-words">{title}</p>
        {meta !== undefined && <span className="shrink-0 text-ui-sm text-muted tabular-nums">{meta}</span>}
      </div>
      {children && <div className="min-w-0 break-words whitespace-pre-wrap">{children}</div>}
      {actions && <div className="flex flex-wrap items-center gap-2 pt-0.5">{actions}</div>}
    </div>
  )
}
