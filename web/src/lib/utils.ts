import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

/** The focus indicator every control wears. An outline rather than a ring:
 * forced-colors mode discards box shadows, and ring classes already mean
 * "selected" on some controls. */
export const focusRing =
  'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring'

/** The box a control you can type into draws: one border, padding and type
 * scale across a text field and a select, so a form reads as one row rather
 * than three. `Input`, `Textarea` and `SelectTrigger` compose it; nothing
 * wears it by hand. `border-input` rather than the base layer's
 * `border-border`: the two part in dark, and a generated shadcn control
 * hardcodes `border-input`. */
export const field = cn(
  focusRing,
  'w-full rounded-md border border-input bg-background px-2 py-1 text-sm',
  'disabled:cursor-not-allowed disabled:opacity-50',
)
