import { clsx, type ClassValue } from 'clsx'
import { extendTailwindMerge } from 'tailwind-merge'

// Without the custom names, tailwind-merge reads `text-ui` as a text colour
// and drops it when a colour class follows, and keeps both of
// `rounded-control rounded-control`.
const twMerge = extendTailwindMerge({
  extend: {
    theme: { radius: ['control', 'panel'], shadow: ['overlay', 'field-focus'] },
    classGroups: { 'font-size': [{ text: ['ui-xs', 'ui-sm', 'ui', 'prose', 'title', 'avatar'] }] },
  },
})

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

/** An outline, not a ring: forced-colors mode discards box shadows, and ring
 * classes already mean "selected" on some controls. */
export const focusRing =
  'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent focus-visible:transition-none'

export const focusRingInset =
  'focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent focus-visible:transition-none'

export const surface = 'rounded-panel border border-seam bg-raised text-text shadow-overlay'

export const fieldFocus =
  'focus-visible:outline-1 focus-visible:-outline-offset-1 focus-visible:outline-accent focus-visible:shadow-field-focus focus-visible:transition-none'

/** Composed by `Input`, `Textarea` and `SelectTrigger`; nothing wears it by hand. */
export const field = cn(
  fieldFocus,
  'h-7 w-full rounded-control border border-control bg-canvas px-2 py-0 text-ui text-text coarse:h-11',
  'placeholder:text-muted aria-[invalid=true]:border-state-failed',
  'disabled:cursor-not-allowed disabled:opacity-50',
  'read-only:cursor-default read-only:bg-chrome read-only:text-muted',
)
