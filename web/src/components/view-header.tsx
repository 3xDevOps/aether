import type { ReactNode } from 'react'
import { ConnectionLine, useConnectionProblem } from '@/components/shell/connection'
import { PaneHeader } from '@/components/ui/pane-header'
import { useIsMobile } from '@/lib/breakpoints'
import { useStore } from '@/store'

/**
 * The header every non-run view draws: a `PaneHeader` that carries the
 * connection problem on desktop and the sidebar opener while it is hidden.
 * On a phone the top bar already shows the title, so the heading is kept
 * for screen readers and focus only.
 */
export function ViewHeader({
  title,
  titleAdornment,
  subtitle,
  actions,
}: {
  title: string
  titleAdornment?: ReactNode
  subtitle?: ReactNode
  actions?: ReactNode
}) {
  const mobile = useIsMobile()
  const collapsed = useStore((s) => s.sidebarCollapsed)
  const toggleSidebar = useStore((s) => s.toggleSidebar)
  const problem = useConnectionProblem()
  if (mobile && !titleAdornment && !actions) {
    return (
      <h1 tabIndex={-1} className="sr-only">
        {title}
      </h1>
    )
  }
  const stateLine = !mobile && problem
    ? <ConnectionLine />
    : !mobile && (typeof subtitle === 'string' ? <span className="truncate text-ui-sm text-muted">{subtitle}</span> : subtitle)
  return (
    <PaneHeader
      title={mobile ? <span className="sr-only">{title}</span> : title}
      stateLine={stateLine || undefined}
      viewSwitch={titleAdornment}
      actions={actions}
      actionsLabel={`${title} actions`}
      onOpenSidebar={!mobile && collapsed ? toggleSidebar : undefined}
    />
  )
}
