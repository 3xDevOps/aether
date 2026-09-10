// What every update banner shares: the strip they render into and the
// dismiss control on its right. Kept apart from update-banner.tsx so the
// per-banner files can import it without importing each other.

import { X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useStore } from '@/store'
import type { UpdateKind } from '@/store/ui'

// Notices keep the icon, summary, action cluster, and dismiss control in
// explicit columns from the existing md workbench breakpoint. At narrow
// widths actions take their own row so long diagnostics never hide them.
export const banner =
  'grid min-w-0 grid-cols-[auto_minmax(0,1fr)_auto] items-start gap-x-2 gap-y-1 border-b border-border bg-card px-3 py-2 text-[13px] md:grid-cols-[auto_minmax(0,1fr)_auto_auto]'

export const bannerContent = 'col-start-2 row-start-1 min-w-0 space-y-1'

export const bannerIcon =
  'mt-px grid size-[22px] shrink-0 place-items-center rounded-sm bg-primary/10 text-primary'

export const bannerActions =
  'col-start-2 row-start-2 flex min-w-0 max-w-full flex-wrap items-center justify-start gap-1 md:col-start-3 md:row-start-1 md:justify-end'

// Technical output is bounded so a failed rebuild cannot push the workbench
// or its actions out of reach. It remains selectable and scrollable in place.
export const verbatim =
  'max-h-28 min-w-0 overflow-auto whitespace-pre-wrap break-words rounded-sm border border-state-failed/30 bg-state-failed/5 px-2 py-1.5 font-mono text-xs leading-5 select-text'

/** The dismiss control every banner carries. */
export function Dismiss({ kind, version }: { kind: UpdateKind; version: string }) {
  const dismiss = useStore((s) => s.dismissUpdate)
  return (
    <Button
      variant="ghost"
      size="icon"
      className="col-start-3 row-start-1 size-6 shrink-0 md:col-start-4"
      aria-label="Dismiss"
      onClick={() => dismiss(kind, version)}
    >
      <X className="size-3.5" aria-hidden />
    </Button>
  )
}
