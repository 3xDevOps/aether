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

/** The house field style. Only the native `<select>` wears it directly; text
 * fields come from `Input` and `Textarea`, which compose it. `border-input`
 * rather than the base layer's `border-border`: the two part in dark, and a
 * generated shadcn control hardcodes `border-input`. */
export const field = cn(
  focusRing,
  'w-full rounded-md border border-input bg-background px-2 py-1 text-sm',
)
