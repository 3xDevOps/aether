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
    <header className="@container/header flex min-h-12 flex-wrap items-center gap-x-3 gap-y-1 border-b px-4 py-2">
      <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-0.5">
        <h1
          className="max-w-64 min-w-0 truncate text-xl font-semibold leading-6 tracking-tight"
          title={title}
        >
          {title}
        </h1>
        {titleAdornment}
        {subtitle && (
          <span
            className="min-w-0 basis-full truncate text-[13px] leading-5 text-muted-foreground @sm/header:basis-auto @sm/header:flex-1"
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
          className="flex shrink-0 items-center gap-1"
        >
          {actions}
        </div>
      )}
    </header>
  )
}
