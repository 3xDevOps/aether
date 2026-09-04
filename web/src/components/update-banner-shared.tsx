// What every update banner shares: the strip they render into and the
// dismiss control on its right. Kept apart from update-banner.tsx so the
// per-banner files can import it without importing each other.

import { X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useStore } from '@/store'
import type { UpdateKind } from '@/store/ui'

export const banner =
  'flex flex-wrap items-start gap-x-3 gap-y-1 border-b bg-card px-3 py-2 text-sm'

/** The dismiss control every banner carries. */
export function Dismiss({ kind, version }: { kind: UpdateKind; version: string }) {
  const dismiss = useStore((s) => s.dismissUpdate)
  return (
    <Button
      variant="ghost"
      size="icon"
      className="ml-auto size-6"
      aria-label="Dismiss"
      onClick={() => dismiss(kind, version)}
    >
      <X className="size-3.5" aria-hidden />
    </Button>
  )
}
