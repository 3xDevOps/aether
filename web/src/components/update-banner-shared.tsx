// What every update banner shares: the strip they render into and the
// dismiss control on its right. Kept apart from update-banner.tsx so the
// per-banner files can import it without importing each other.

import { X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useStore } from '@/store'
import type { UpdateKind } from '@/store/ui'

// One row, never wrapped: the prose column is the half that gives way.
export const banner =
  'flex items-start gap-3 border-b bg-card px-3 py-2 text-sm'

// Output a prompt shows verbatim, at whatever length it arrives. The
// prompts stack, so an unbounded one scrolls the next prompt's controls out
// of the strip.
export const verbatim = 'max-h-24 overflow-y-auto break-words font-mono text-xs'

/** The dismiss control every banner carries. */
export function Dismiss({ kind, version }: { kind: UpdateKind; version: string }) {
  const dismiss = useStore((s) => s.dismissUpdate)
  return (
    <Button
      variant="ghost"
      size="icon"
      className="size-6 shrink-0"
      aria-label="Dismiss"
      onClick={() => dismiss(kind, version)}
    >
      <X className="size-3.5" aria-hidden />
    </Button>
  )
}
