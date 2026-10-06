import type * as React from 'react'
import { cn } from '@/lib/utils'

export function EmptyState({
  title,
  children,
  action,
  className,
}: {
  title: React.ReactNode
  children?: React.ReactNode
  action?: React.ReactNode
  className?: string
}) {
  return (
    <div
      data-slot="empty-state"
      className={cn('mx-auto flex max-w-sm flex-col items-center gap-2 px-4 py-12 text-center', className)}
    >
      <h2 className="text-title text-text">{title}</h2>
      {children && <p className="text-ui text-muted">{children}</p>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  )
}
