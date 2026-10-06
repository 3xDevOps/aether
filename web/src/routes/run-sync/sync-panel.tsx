// Mirrors sync.status into the store so the board badge and this panel agree.

import { useCallback, useEffect, useRef, useState } from 'react'
import { CircleAlert, CircleCheck, RefreshCw } from '@/components/icons'
import { message } from '@/lib/format'
import type { CardSlotProps } from '@/components/slots'
import { Button } from '@/components/ui/button'
import { api, type Api } from '@/lib/api'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

export function SyncBadge({ run }: CardSlotProps) {
  const state = useStore((s) => s.syncSessions[run.id]?.state)
  const navigate = useStore((s) => s.navigate)
  if (state !== 'running') return null

  return (
    <Button variant="link" size="sm" hint="Sync overlay running" onClick={() => navigate('settings', {})}>
      <RefreshCw />
      Syncing
    </Button>
  )
}

/** A refused start keeps the server's message next to a Force retry, the
 * escape hatch for an overlay checkout with local changes. */
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

  // After unmount no async refresh may write the store, or a stale snapshot
  // could overwrite a freshly-mounted panel's. A ref so the verbs share it.
  const cancelled = useRef(false)
  useEffect(() => {
    cancelled.current = false
    return () => {
      cancelled.current = true
    }
  }, [])

  const refresh = useCallback(async () => {
    try {
      const { sessions } = await client.localSyncStatus()
      if (!cancelled.current) setSyncSessions(sessions)
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
    <section aria-label="Sync" className="min-w-0 space-y-3">
      <div className="flex min-w-0 flex-wrap items-center justify-between gap-3">
        <span className="inline-flex min-w-0 items-center gap-1.5 text-ui-sm text-muted">
          {active ? (
            <RefreshCw className="size-3.5 shrink-0 text-state-working" aria-hidden />
          ) : (
            <CircleCheck className="size-3.5 shrink-0" aria-hidden />
          )}
          {!session ? 'Not mirroring' : session.state === 'running' ? 'Mirroring' : `Mirror ${session.state}`}
        </span>
        {active ? (
          <Button size="sm" variant="secondary" disabled={busy} onClick={() => void stop()}>
            Stop mirroring
          </Button>
        ) : (
          <Button size="sm" variant="secondary" disabled={busy} onClick={() => void start()}>
            Start mirroring
          </Button>
        )}
      </div>
      {session?.state === 'conflict' && session.conflict && (
        <div className="border-l-2 border-state-needs-you/60 bg-state-needs-you/10 px-3 py-2">
          <p className="flex items-start gap-2 text-ui-sm font-medium text-state-needs-you">
            <CircleAlert className="mt-0.5 size-3.5 shrink-0" aria-hidden />
            <span>{session.conflict}</span>
          </p>
          <p className="mt-1 pl-5 text-ui-sm leading-5 text-muted">
            The conflict was reported to the server; the session is paused until
            it is resolved.
          </p>
        </div>
      )}
      {error && (
        <div role="alert" className="border-l-2 border-state-failed/60 bg-state-failed/10 px-3 py-2">
          <p className="text-ui-sm text-state-failed">{error.text}</p>
          {error.verb === 'start' && (
            <Button
              size="sm"
              variant="secondary"
              className="mt-2"
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
