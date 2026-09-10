import type { ReactNode } from 'react'

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
    <header className="@container/header flex min-h-[35px] flex-wrap items-center gap-x-3 gap-y-1 border-b px-3 py-1 sm:px-4">
      <div className="flex min-w-0 flex-[1_1_20rem] flex-wrap items-center gap-x-2 gap-y-0.5">
        <h1
          className="min-w-0 break-words text-[15px] font-semibold leading-5"
          title={title}
        >
          {title}
        </h1>
        {titleAdornment}
        {subtitle && (
          <span
            className="min-w-0 basis-full break-words text-xs leading-4 text-muted-foreground @sm/header:basis-auto @sm/header:flex-1"
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
          className="flex max-w-full shrink-0 flex-wrap items-center justify-end gap-1"
        >
          {actions}
        </div>
      )}
    </header>
  )
}
