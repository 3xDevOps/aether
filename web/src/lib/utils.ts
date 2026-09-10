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

export const field = cn(
  focusRing,
  'w-full h-9 min-h-9 rounded-sm border border-input bg-background px-3 py-2 text-sm leading-5',
)
