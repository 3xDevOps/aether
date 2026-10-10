import type * as React from 'react'
import { cn, focusRingInset } from '@/lib/utils'

export function ListRow({
  leading,
  trailing,
  action,
  hoverAction,
  selected = false,
  className,
  children,
  ...props
}: React.ComponentProps<'button'> & {
  leading?: React.ReactNode
  trailing?: React.ReactNode
  action?: React.ReactNode
  hoverAction?: React.ReactNode
  selected?: boolean
}) {
  return (
    <div
      data-slot="list-row"
      data-selected={selected || undefined}
      className={cn(
        'group/row relative flex h-7 min-w-0 items-center rounded-control text-ui text-text coarse:h-11',
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
          // The button's hit area covers what `action` only displays; its buttons sit above it.
          action && 'after:absolute after:inset-0',
        )}
        {...props}
      >
        {leading}
        <span className="min-w-0 flex-1 truncate">{children}</span>
        {trailing !== undefined && (
          <span className={cn('shrink-0 text-ui-sm tabular-nums', selected ? 'text-text' : 'text-muted')}>{trailing}</span>
        )}
      </button>
      {action && <div className="pointer-events-none relative flex shrink-0 items-center gap-2 pr-2 text-muted [&_button]:pointer-events-auto">{action}</div>}
      {hoverAction && (
        <div className="relative w-0 shrink-0 overflow-hidden group-focus-within/row:w-auto group-focus-within/row:overflow-visible group-hover/row:w-auto has-[[data-state=open]]:w-auto coarse:w-auto coarse:overflow-visible">
          <div className="flex items-center pr-1">{hoverAction}</div>
        </div>
      )}
    </div>
  )
}
