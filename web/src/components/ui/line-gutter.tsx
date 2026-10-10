import type * as React from 'react'
import { cn, focusRingInset } from '@/lib/utils'

/**
 * A code line's number gutter, which takes a click. A `+` shows in its last
 * 20px while the line (`group/line`) is hovered or the gutter has focus, so a
 * line nobody points at carries no extra ink.
 *
 * Only the `focusable` one is a button: its list keeps one per file and moves
 * it with the arrow keys, so a long file adds one control, not one per line.
 */
export function LineGutter({ label, focusable, children }: { label: string; focusable: boolean; children: React.ReactNode }) {
  const className = cn(
    'relative flex shrink-0',
    "after:absolute after:top-0.5 after:right-0.5 after:size-4 after:rounded-control after:bg-accent after:text-center after:font-sans after:text-ui after:leading-4 after:font-medium after:text-on-accent after:opacity-0 after:content-['+']",
    'group-hover/line:after:opacity-100',
  )
  return focusable ? (
    <button type="button" data-slot="line-gutter" aria-label={label} className={cn(focusRingInset, 'text-left focus:after:opacity-100', className)}>
      {children}
    </button>
  ) : (
    <div data-slot="line-gutter" className={className}>
      {children}
    </div>
  )
}
