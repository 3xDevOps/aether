import { clsx, type ClassValue } from 'clsx'
import { extendTailwindMerge } from 'tailwind-merge'

// Without the custom names, tailwind-merge reads `text-ui` as a text colour
// and drops it when a colour class follows, and keeps both of
// `rounded-control rounded-panel` or `animate-overlay-in animate-none`.
const twMerge = extendTailwindMerge({
  extend: {
    theme: {
      radius: ['chip', 'control', 'panel', 'composer'],
      shadow: ['card', 'card-hover', 'button', 'button-hover', 'overlay', 'dialog', 'field-focus'],
      ease: ['hover', 'standard', 'enter', 'glide', 'press', 'pop', 'sheet', 'pulse'],
      animate: [
        'overlay-in',
        'fade-in',
        'dialog-in',
        'scrim-in',
        'sheet-up',
        'sheet-left',
        'sheet-right',
        'expand-right',
        'collapsible-down',
        'collapsible-up',
        'entrance',
        'skeleton-sweep',
        'progress-sweep',
        'pop-in',
        'shake',
        'bob',
      ],
    },
    classGroups: {
      'font-size': [{ text: ['ui-xs', 'ui-sm', 'ui', 'prose', 'title', 'avatar', 'stat'] }],
      duration: [{ duration: ['instant', 'hover', 'press', 'release', 'popover', 'glide', 'expand', 'pulse'] }],
    },
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

/** A disclosure marker that turns as its body opens. */
export const chevronTurn = 'transition-transform duration-200 ease-standard motion-reduce:transition-none'

export const surface = 'rounded-panel border border-seam bg-raised text-text shadow-overlay'

export const fieldFocus =
  'focus-visible:outline-1 focus-visible:-outline-offset-1 focus-visible:outline-accent focus-visible:shadow-field-focus focus-visible:transition-none'

/** Composed by `Input`, `Textarea`, `SelectTrigger` and `PopoverFieldTrigger`; nothing wears it by hand. */
export const field = cn(
  fieldFocus,
  'h-7 w-full rounded-control border border-control bg-canvas px-2 py-0 text-ui text-text coarse:h-11',
  'placeholder:text-muted aria-[invalid=true]:border-state-failed',
  'disabled:cursor-not-allowed disabled:opacity-50',
  'read-only:cursor-default read-only:bg-chrome read-only:text-muted',
)
