// The live sync overlay for one run: the local gateway mirrors the run's
// worktree into the linked repository in the background. This panel owns the
// sync.* verbs for a single run and mirrors sync.status into the store, so
// the board badge and this view agree on what is running.

import { RefreshCw } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { message } from '@/components/palette/palette'
import type { CardSlotProps } from '@/components/slots'
import { Button } from '@/components/ui/button'
import { api, type Api } from '@/lib/api'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

/** A run card's marker: this run's worktree is being mirrored right now. */
export function SyncBadge({ run }: CardSlotProps) {
  const state = useStore((s) => s.syncSessions[run.id]?.state)
  const navigate = useStore((s) => s.navigate)
  if (state !== 'running') return null

  return (
    <button
      type="button"
      aria-label="Sync overlay running"
      title="Sync overlay running"
      onClick={() => navigate('settings', {})}
      className="flex shrink-0 items-center rounded-sm bg-state-working/15 px-1 text-[11px] text-state-working"
    >
      <RefreshCw className="size-3.5" aria-hidden />
    </button>
  )
}

/**
 * One run's sync session: its state, its conflict if paused, and the
 * start/stop verbs. A refused start keeps the server's message on screen
 * next to a Force retry - sync.start's escape hatch for an overlay checkout
 * with local changes.
 */
export function SyncPanel({
  runID,
  client = api,
}: {
  runID: string
  client?: Api
}) {
  const caps = useCapability()
  const session = useStore((s) => s.syncSessions[runID])
  const setSyncSessions = useStore((s) => s.setSyncSessions)
  const [error, setError] = useState<{ verb: 'start' | 'stop'; text: string } | null>(
    null,
  )
  const [busy, setBusy] = useState(false)

  const refresh = useCallback(async () => {
    try {
      setSyncSessions((await client.localSyncStatus()).sessions)
    } catch {
      // A failed poll is transient; the verbs surface their own refusals.
    }
  }, [client, setSyncSessions])

  const enabled = caps.hasLocal('sync.start')
  useEffect(() => {
    if (!enabled) return
    // /local/v1 has no push channel, polling is the only signal.
    void refresh()
    const timer = setInterval(() => void refresh(), 5000)
    return () => clearInterval(timer)
  }, [enabled, refresh])

  if (!enabled) return null

  const start = async (force?: boolean) => {
    setBusy(true)
    setError(null)
    try {
      if (force) await client.localSyncStart(runID, true)
      else await client.localSyncStart(runID)
      await refresh()
    } catch (err) {
      setError({ verb: 'start', text: message(err) })
    } finally {
      setBusy(false)
    }
  }

  const stop = async () => {
    setBusy(true)
    setError(null)
    try {
      await client.localSyncStop(runID)
      await refresh()
    } catch (err) {
      setError({ verb: 'stop', text: message(err) })
    } finally {
      setBusy(false)
    }
  }

  const active = session?.state === 'running' || session?.state === 'conflict'

  return (
    <section aria-label="Sync" className="space-y-2">
      <p className="text-sm">
        {session ? `Overlay ${session.state}` : 'No sync session for this run.'}
      </p>
      {session?.state === 'conflict' && session.conflict && (
        <div className="space-y-1">
          <p className="text-xs text-state-needs-attention">{session.conflict}</p>
          <p className="text-xs text-muted-foreground">
            The conflict was reported to the server; the session is paused until
            it is resolved.
          </p>
        </div>
      )}
      <div className="flex gap-2">
        {active ? (
          <Button size="sm" variant="outline" disabled={busy} onClick={() => void stop()}>
            Stop
          </Button>
        ) : (
          <Button size="sm" disabled={busy} onClick={() => void start()}>
            Start
          </Button>
        )}
      </div>
      {error && (
        <div className="space-y-1">
          <p className="text-xs text-state-failed">{error.text}</p>
          {error.verb === 'start' && (
            <Button
              size="sm"
              variant="outline"
              disabled={busy}
              onClick={() => void start(true)}
            >
              Force
            </Button>
          )}
        </div>
      )}
    </section>
  )
}
