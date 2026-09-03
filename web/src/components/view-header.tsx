import type { ReactNode } from 'react'

/**
 * The title row every view opens with. `actions` is the right-hand slot the
 * run detail fills with its action bar; it scrolls rather than growing, so a
 * long verb list never pushes the title out of the row.
 */
export function ViewHeader({
  title,
  subtitle,
  actions,
}: {
  title: string
  subtitle?: string
  actions?: ReactNode
}) {
  return (
    <header className="flex h-9 items-center gap-2 border-b px-4">
      <h1 className="truncate text-sm font-medium">{title}</h1>
      {subtitle && (
        <span className="shrink-0 text-xs text-muted-foreground">{subtitle}</span>
      )}
      {actions && (
        <div
          role="toolbar"
          aria-label={`${title} actions`}
          className="ml-auto flex min-w-0 items-center gap-0.5 overflow-x-auto"
        >
          {actions}
        </div>
      )}
    </header>
  )
}
