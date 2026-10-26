import type * as React from 'react'
import { cn } from '@/lib/utils'

export function StatTile({
  label,
  value,
  compact = false,
  spark,
  role = 'article',
  children,
}: {
  label: string
  value: React.ReactNode
  /** A line of words rather than one number. */
  compact?: boolean
  spark?: React.ReactNode
  role?: 'article' | 'group'
  children?: React.ReactNode
}) {
  return (
    <div
      data-slot="stat-tile"
      role={role}
      aria-label={label}
      className="flex min-w-0 flex-col gap-1 rounded-panel border border-seam p-4"
    >
      <h3 className="text-ui-sm text-muted">{label}</h3>
      <p className={cn('tabular-nums', compact ? 'text-prose font-medium' : 'text-stat')}>{value}</p>
      {children && <div className="flex flex-col gap-0.5 text-ui-sm text-muted">{children}</div>}
      {spark && <div className="mt-auto pt-2">{spark}</div>}
    </div>
  )
}
