import type * as React from 'react'
import { IconTile } from '@/components/ui/icon-tile'
import { cn } from '@/lib/utils'

export function EmptyState({
  icon,
  title,
  children,
  action,
  className,
}: {
  icon?: React.ReactNode
  title: React.ReactNode
  children?: React.ReactNode
  action?: React.ReactNode
  className?: string
}) {
  return (
    <div
      data-slot="empty-state"
      className={cn('mx-auto flex max-w-sm animate-fade-in flex-col items-center gap-1.5 px-4 py-16 text-center motion-reduce:animate-none', className)}
    >
      {icon && <IconTile className="mb-2 size-8 rounded-panel [&_svg]:size-4">{icon}</IconTile>}
      <h2 className="text-title text-text">{title}</h2>
      {children && <p className="text-ui text-muted">{children}</p>}
      {action && <div className="mt-3">{action}</div>}
    </div>
  )
}
