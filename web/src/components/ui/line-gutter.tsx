import type * as React from 'react'
import { cn, focusRingInset } from '@/lib/utils'

/**
 * A code line's number gutter as one button. Its children stay as they are;
 * a `+` shows in its last 20px while the line (`group/line`) is hovered or
 * the button has focus, so a line nobody points at carries no extra ink.
 */
export function LineGutter({ className, ...props }: React.ComponentProps<'button'>) {
  return (
    <button
      type="button"
      data-slot="line-gutter"
      className={cn(
        focusRingInset,
        'relative flex shrink-0 text-left',
        "after:absolute after:top-0.5 after:right-0.5 after:size-4 after:rounded-control after:bg-accent after:text-center after:font-sans after:text-ui after:leading-4 after:font-medium after:text-on-accent after:opacity-0 after:content-['+']",
        'group-hover/line:after:opacity-100 focus:after:opacity-100',
        className,
      )}
      {...props}
    />
  )
}
