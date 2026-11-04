import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { useClock } from '@/lib/clock'
import { timeAgo } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'

/**
 * One line while the dashboard shows the saved snapshot or a refresh failed:
 * when the state is from and the failure verbatim (tap to read all of it),
 * with a retry. Retry fires the same `online` wake the stream and hydration
 * already listen for. The live region holds only the error, so the ageing
 * time is never re-announced.
 */
export function StaleBanner() {
  const staleSince = useStore((s) => s.staleSince)
  const error = useStore((s) => s.hydrationError)
  const connection = useStore((s) => s.connection)
  const now = useClock()
  const [open, setOpen] = useState(false)
  if (staleSince === null && error === null) return null
  const saved = staleSince === null ? '' : `Saved state from ${timeAgo(new Date(staleSince).toISOString(), Math.max(now, staleSince))}`
  const state = error ?? (connection === 'live' ? 'refreshing' : 'reconnecting')
  return (
    <div className="flex min-w-0 items-start gap-2 border-b border-seam bg-chrome px-3 py-1 text-ui-sm text-muted">
      <div className="min-w-0 flex-1 py-1.5">
        {saved && <p className="text-text">{saved}</p>}
        <Button
          variant="ghost"
          size="sm"
          aria-expanded={open}
          onClick={() => setOpen((v) => !v)}
          className="h-auto min-h-6 w-full min-w-0 justify-start px-0 py-1 text-left font-normal coarse:h-auto coarse:min-h-11 coarse:px-0"
        >
          <span role="status" className={cn('block w-full min-w-0 text-left', open ? 'whitespace-pre-wrap break-words' : 'truncate')}>{state}</span>
        </Button>
      </div>
      <Button size="sm" variant="secondary" className="shrink-0" onClick={() => window.dispatchEvent(new Event('online'))}>
        Retry
      </Button>
    </div>
  )
}
