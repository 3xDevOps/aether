import type * as React from 'react'
import { PanelLeft } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

export function PaneHeader({
  title,
  size = 'view',
  stateLine,
  viewSwitch,
  actions,
  actionsLabel = 'View actions',
  onOpenSidebar,
  titleRef,
  className,
}: {
  title: React.ReactNode
  size?: 'view' | 'run'
  stateLine?: React.ReactNode
  viewSwitch?: React.ReactNode
  actions?: React.ReactNode
  actionsLabel?: string
  onOpenSidebar?: () => void
  titleRef?: React.Ref<HTMLHeadingElement>
  className?: string
}) {
  return (
    <header
      data-slot="pane-header"
      className={cn(
        'flex shrink-0 items-center gap-2 border-b border-seam bg-canvas px-4 coarse:px-2',
        size === 'run' ? 'h-14' : 'h-11',
        className,
      )}
    >
      {onOpenSidebar && (
        <Button variant="ghost" size="icon" label="Open sidebar" onClick={onOpenSidebar} className="-ml-2">
          <PanelLeft />
        </Button>
      )}
      <div className="flex min-w-0 flex-1 flex-col justify-center">
        <h1 ref={titleRef} tabIndex={-1} className="truncate text-title outline-none">
          {title}
        </h1>
        {stateLine}
      </div>
      {viewSwitch}
      {actions && (
        <div role="toolbar" aria-label={actionsLabel} className="flex shrink-0 items-center gap-1">
          {actions}
        </div>
      )}
    </header>
  )
}
