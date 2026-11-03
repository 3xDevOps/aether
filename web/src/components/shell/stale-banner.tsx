import { Button } from '@/components/ui/button'
import { timeAgo } from '@/lib/format'
import { useStore } from '@/store'

/**
 * One line while the dashboard shows the saved snapshot or a refresh failed:
 * when the state is from and the failure verbatim, with a retry. Retry fires
 * the same `online` event the stream and hydration already wake on.
 */
export function StaleBanner() {
  const staleSince = useStore((s) => s.staleSince)
  const error = useStore((s) => s.hydrationError)
  const connection = useStore((s) => s.connection)
  if (staleSince === null && error === null) return null
  const saved = staleSince === null ? '' : `Saved state from ${timeAgo(new Date(staleSince).toISOString())}`
  const state = error ?? (connection === 'live' ? 'refreshing' : 'reconnecting')
  return (
    <div
      role="status"
      className="flex min-w-0 items-center gap-2 border-b border-seam bg-chrome px-3 py-1 text-ui-sm text-muted"
    >
      <p className="min-w-0 flex-1 truncate" title={error ?? undefined}>
        {saved && <span className="text-text">{saved} · </span>}
        {state}
      </p>
      <Button size="sm" variant="secondary" onClick={() => window.dispatchEvent(new Event('online'))}>
        Retry
      </Button>
    </div>
  )
}
