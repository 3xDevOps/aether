import type * as React from 'react'
import { cn, focusRingInset } from '@/lib/utils'

export function ListRow({
  leading,
  trailing,
  hoverAction,
  selected = false,
  className,
  children,
  ...props
}: React.ComponentProps<'button'> & {
  leading?: React.ReactNode
  trailing?: React.ReactNode
  hoverAction?: React.ReactNode
  selected?: boolean
}) {
  return (
    <div
      data-slot="list-row"
      data-selected={selected || undefined}
      className={cn(
        'group/row flex h-7 min-w-0 items-center rounded-control text-ui text-text coarse:h-11',
        selected ? 'bg-selection' : 'hover:bg-hover-chrome',
        className,
      )}
    >
      <button
        type="button"
        aria-current={selected || undefined}
        className={cn(
          focusRingInset,
          'flex h-full min-w-0 flex-1 items-center gap-2 rounded-control px-2 text-left [&_svg]:shrink-0 [&_svg:not([data-slot=status-dot])]:size-3.5 [&_svg:not([data-slot=status-dot])]:text-muted',
        )}
        {...props}
      >
        {leading}
        <span className="min-w-0 flex-1 truncate">{children}</span>
        {trailing !== undefined && (
          <span className={cn('shrink-0 text-ui-sm tabular-nums', selected ? 'text-text' : 'text-muted')}>{trailing}</span>
        )}
      </button>
      {hoverAction && (
        <div className="hidden shrink-0 items-center pr-1 group-focus-within/row:flex group-hover/row:flex has-[[data-state=open]]:flex coarse:flex">
          {hoverAction}
        </div>
      )}
    </div>
  )
}
