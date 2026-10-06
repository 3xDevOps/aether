import type { ReactNode } from 'react'

/** Sticky because a step can be taller than the window, and Back must stay in reach. */
export const actionRow =
  'sticky bottom-0 z-10 mt-2 flex flex-wrap items-center gap-2 border-t border-seam bg-canvas py-3'

export const pane =
  'max-h-64 min-w-0 overflow-x-auto overflow-y-auto px-3 py-2 font-code text-ui-sm whitespace-pre-wrap break-words'

export function Step({
  label,
  title,
  lead,
  actions,
  children,
}: {
  label: string
  title: string
  lead?: ReactNode
  actions?: ReactNode
  children?: ReactNode
}) {
  return (
    <section aria-label={label} className="flex min-w-0 flex-col gap-5">
      <div className="flex flex-col gap-1">
        <h2 className="text-title text-text">{title}</h2>
        {lead && <p className="text-ui text-muted">{lead}</p>}
      </div>
      {children}
      {actions && <div className={actionRow}>{actions}</div>}
    </section>
  )
}
