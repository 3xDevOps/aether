import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

/** The focus indicator every control wears. An outline rather than a ring:
 * forced-colors mode discards box shadows, and ring classes already mean
 * "selected" on some controls. */
export const focusRing =
  'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring focus-visible:transition-none'

/** The box a control you can type into draws: one border, padding and type
 * scale across a text field and a select, so a form reads as one row rather
 * than three. `Input`, `Textarea` and `SelectTrigger` compose it; nothing
 * wears it by hand. The control border intentionally uses `border-input`
 * instead of the pane-level `border-border`, while `bg-field` keeps the
 * field surface explicit in both themes. */
export const field = cn(
  focusRing,
  'h-[26px] min-h-[26px] w-full rounded-sm border border-input bg-field px-2 py-0 text-[13px] leading-6',
  'disabled:cursor-not-allowed disabled:pointer-events-none disabled:opacity-50 disabled:bg-field',
  'read-only:cursor-default read-only:bg-field read-only:opacity-100',
)
