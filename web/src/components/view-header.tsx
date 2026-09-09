import type { ReactNode } from 'react'

/**
 * The title row every view opens with. Nothing here scrolls sideways, so
 * anything past the right edge is unreachable: the actions keep their width
 * and the title truncates instead.
 */
export function ViewHeader({
  title,
  titleAdornment,
  subtitle,
  actions,
}: {
  title: string
  titleAdornment?: ReactNode
  subtitle?: string
  actions?: ReactNode
}) {
  return (
    <header className="@container/header flex h-9 items-center gap-2 border-b px-4">
      <div className="flex min-w-0 flex-1 items-center gap-2">
        <h1 className="max-w-64 min-w-0 truncate text-sm font-medium" title={title}>
          {title}
        </h1>
        {titleAdornment}
        {subtitle && (
          <span
            className="min-w-0 flex-1 truncate text-xs text-muted-foreground"
            title={subtitle}
          >
            {subtitle}
          </span>
        )}
      </div>
      {actions && (
        <div
          role="toolbar"
          aria-label={`${title} actions`}
          className="flex shrink-0 items-center gap-0.5"
        >
          {actions}
        </div>
      )}
    </header>
  )
}
