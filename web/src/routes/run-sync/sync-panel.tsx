// The live sync overlay for one run: the local gateway mirrors the run's
// worktree into the linked repository in the background. This panel owns the
// sync.* verbs for a single run and mirrors sync.status into the store, so the
// board badge and this view agree on what is running.

import { CircleAlert, CheckCircle2, RefreshCw } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { message } from '@/lib/format'
import type { CardSlotProps } from '@/components/slots'
import { Button } from '@/components/ui/button'
import { Tooltip } from '@/components/ui/heroui'
import { api, type Api } from '@/lib/api'
import { cn, focusRing } from '@/lib/utils'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

/** A run card's marker: this run's worktree is being mirrored right now. */
export function SyncBadge({ run }: CardSlotProps) {
  const state = useStore((s) => s.syncSessions[run.id]?.state)
  const navigate = useStore((s) => s.navigate)
  if (state !== 'running') return null

  return (
    <Tooltip>
      <Tooltip.Trigger<'button'>
        render={(triggerProps) => (
          <button
            {...triggerProps}
            type="button"
            aria-label="Sync overlay running"
            onClick={() => {
              navigate('settings', {})
            }}
            className={cn(
              focusRing,
              'flex shrink-0 items-center gap-1 rounded-sm bg-state-working/15 px-1.5 py-0.5 text-[11px] text-state-working',
            )}
          >
            <RefreshCw className="size-3.5" aria-hidden />
            <span className="sr-only">Running</span>
          </button>
        )}
      />
      <Tooltip.Content>Sync overlay running</Tooltip.Content>
    </Tooltip>
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

  // The interval tick and the verbs' own refreshes all resolve async: after
  // unmount none of them may write the store, or a stale snapshot could
  // overwrite what a freshly-mounted panel just fetched. Same cancelled
  // convention as the LinkCard/DaemonCard/members effects, held in a ref
  // because the verbs share it with the polling effect.
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
    <section aria-label="Sync" className="min-w-0 space-y-3 border-t border-border/70 pt-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <h2 className="text-sm font-medium">Local sync overlay</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            Mirror this run's worktree into the linked repository.
          </p>
        </div>
        <span className="inline-flex max-w-full min-w-0 items-center gap-1.5 border border-border/70 bg-muted px-2 py-1 text-xs font-medium">
          {active ? (
            <RefreshCw className="size-3.5 text-state-working" aria-hidden />
          ) : (
            <CheckCircle2 className="size-3.5 text-muted-foreground" aria-hidden />
          )}
          {session ? `Overlay ${session.state}` : 'No sync session for this run.'}
        </span>
      </div>
      {session?.state === 'conflict' && session.conflict && (
        <div className="border-l-2 border-state-needs-attention/60 bg-state-needs-attention/10 px-3 py-2">
          <p className="flex items-start gap-2 text-xs font-medium text-state-needs-attention">
            <CircleAlert className="mt-0.5 size-3.5 shrink-0" aria-hidden />
            <span>{session.conflict}</span>
          </p>
          <p className="mt-1 pl-5 text-xs leading-5 text-muted-foreground">
            The conflict was reported to the server; the session is paused until
            it is resolved.
          </p>
        </div>
      )}
      <div className="flex flex-wrap gap-2">
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
        <div role="alert" className="border-l-2 border-state-failed/60 bg-state-failed/10 px-3 py-2">
          <p className="text-xs text-state-failed">{error.text}</p>
          {error.verb === 'start' && (
            <Button
              size="sm"
              variant="outline"
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
