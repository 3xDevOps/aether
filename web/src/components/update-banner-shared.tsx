// What every update banner shares: the strip they render into and the
// dismiss control on its right. Kept apart from update-banner.tsx so the
// per-banner files can import it without importing each other.

import { X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useStore } from '@/store'
import type { UpdateKind } from '@/store/ui'

// Notices keep the icon, summary, actions, and dismiss control in four
// explicit columns. This leaves the action cluster beside the summary on
// desktop instead of letting a long explanation force it onto a later row.
export const banner =
  'grid grid-cols-[auto_minmax(0,1fr)_minmax(0,auto)_auto] items-start gap-x-3 gap-y-2 border-b border-border/80 bg-card px-4 py-2.5 text-sm shadow-surface'

export const bannerContent = 'min-w-0 flex-1 space-y-1.5'

export const bannerIcon =
  'mt-0.5 grid size-7 shrink-0 place-items-center rounded-md bg-primary/10 text-primary'

export const bannerActions =
  'col-start-3 row-start-1 flex min-w-0 max-w-full shrink-0 flex-wrap items-center justify-end gap-2'

// Output a prompt shows verbatim, at whatever length it arrives. The
// bounded block keeps a failed rebuild from pushing every other prompt out of
// view while preserving the complete message in its own scroll area.
export const verbatim =
  'max-h-24 overflow-y-auto break-words rounded-sm border border-state-failed/30 bg-state-failed/5 px-2 py-1.5 font-mono text-xs leading-5'

/** The dismiss control every banner carries. */
export function Dismiss({ kind, version }: { kind: UpdateKind; version: string }) {
  const dismiss = useStore((s) => s.dismissUpdate)
  return (
    <Button
      variant="ghost"
      size="icon"
      className="col-start-4 row-start-1 size-8 shrink-0"
      aria-label="Dismiss"
      onClick={() => dismiss(kind, version)}
    >
      <X className="size-3.5" aria-hidden />
    </Button>
  )
}
