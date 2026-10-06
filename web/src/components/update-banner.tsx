import { useCallback, useEffect, useRef, useState } from 'react'
import { CircleAlert, MonitorCog, ServerCog } from 'lucide-react'
import { CliBanner } from '@/components/cli-update-banner'
import { CopyableCommand } from '@/components/copyable-command'
import { desktopBridge } from '@/components/shell/window-bar'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  banner,
  bannerActions,
  bannerContent,
  bannerIcon,
  Dismiss,
  verbatim,
} from '@/components/update-banner-shared'
import { api, type Api } from '@/lib/api'
import { bareVersion, message } from '@/lib/format'
import type {
  ServerUpdatePayload,
  ServerUpdateStatus,
  ServerUpdateWaiting,
  ServerUpdateWhen,
} from '@/lib/types'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import { useCapability, useIsAdmin } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import type { UpdateKind } from '@/store/ui'

export const RECHECK_MS = 30 * 60 * 1000

function shellIsStale(cliVersion: string | undefined): boolean {
  const shell = desktopBridge()?.shellVersion
  if (!shell || !cliVersion) return false
  return bareVersion(shell) !== bareVersion(cliVersion)
}

export function UpdateCenter({ client = api }: { client?: Api } = {}) {
  const caps = useCapability()
  const serves = caps.hasLocal('update.check')
  const readsServerUpdate = caps.hasMethod('server.update_status')
  const update = useStore((s) => s.update)
  const setUpdate = useStore((s) => s.setUpdate)
  const setServerUpdate = useStore((s) => s.setServerUpdate)
  const setServerUpdateFailed = useStore((s) => s.setServerUpdateFailed)
  // A self-updating server re-executes and drops the socket; the read on
  // reconnect is what clears the prompt.
  const serverVersion = useStore((s) => s.info?.server_version)
  const connection = useStore((s) => s.connection)
  const [statusReads, setStatusReads] = useState(0)

  const live = useRef(true)
  const pending = useRef(0)
  const issued = useRef(0)
  useEffect(() => {
    live.current = true
    return () => {
      live.current = false
    }
  }, [])

  // Only the newest read writes, so a click supersedes reads in flight.
  // `pending` counts because reads overlap.
  const recheck = useCallback(
    async (refresh?: boolean) => {
      const id = ++issued.current
      pending.current += 1
      try {
        const status = await client.localUpdateCheck(refresh)
        if (live.current && id === issued.current) setUpdate(status)
        return status
      } finally {
        pending.current -= 1
      }
    },
    [client, setUpdate],
  )

  useEffect(() => {
    if (!serves) return
    const read = () => {
      if (pending.current > 0 || document.visibilityState === 'hidden') return
      // The prompt must keep naming the release being installed.
      if (useStore.getState().installingUpdate) return
      void recheck()
        // A background lookup the member did not ask for fails silently.
        .catch(() => {})
    }
    read()
    const timer = setInterval(read, RECHECK_MS)
    window.addEventListener('focus', read)
    document.addEventListener('visibilitychange', read)
    return () => {
      clearInterval(timer)
      window.removeEventListener('focus', read)
      document.removeEventListener('visibilitychange', read)
    }
  }, [serves, recheck])

  useEffect(() => {
    if (!readsServerUpdate || !serverVersion) return
    let live = true
    void client
      .serverUpdateStatus()
      .then((status) => {
        if (live) setServerUpdate(status)
      })
      .catch((err) => {
        if (live) setServerUpdateFailed(message(err))
      })
    return () => {
      live = false
    }
  }, [
    readsServerUpdate,
    serverVersion,
    connection,
    statusReads,
    client,
    setServerUpdate,
    setServerUpdateFailed,
  ])

  const open = useStore((s) => s.updatesOpen)
  const setOpen = useStore((s) => s.setUpdatesOpen)
  const anything = useUpdateNotice() !== null
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Updates</DialogTitle>
          {!anything && <DialogDescription>Nothing to update.</DialogDescription>}
        </DialogHeader>
        <div className="min-w-0">
          {serves && <ShellBanner />}
          {serves && update && (
            <CliBanner update={update} client={client} recheck={recheck} />
          )}
          <ServerBanner client={client} onRetry={() => setStatusReads((n) => n + 1)} />
        </div>
      </DialogContent>
    </Dialog>
  )
}

/** A server update already moving is named even when dismissed: it is about to restart the server. */
export function useUpdateNotice(includeDismissed = false): { text: string; action: boolean } | null {
  const caps = useCapability()
  const isAdmin = useIsAdmin()
  const update = useStore((s) => s.update)
  const status = useStore((s) => s.serverUpdate)
  const progress = useStore((s) => s.serverUpdateProgress)
  const stored = useStore((s) => s.dismissedUpdates)
  const dismissed = includeDismissed ? noneDismissed : stored
  const cliVersion = useStore((s) => s.capabilities?.version)
  const serves = caps.hasLocal('update.check')
  const latest = update?.cli.latest ?? ''
  if (serves && update?.cli.update_available && latest && dismissed.cli !== latest) {
    return { text: `Aether ${bareVersion(latest)} is available`, action: true }
  }
  const flow = serverFlow(status, progress)
  const serverLatest = status?.latest || update?.cli.latest || ''
  const behind = status ? status.update_available : (update?.server_behind ?? false)
  if (flow.name === 'scheduled' || flow.name === 'applying' || flow.name === 'restarting') {
    return { text: 'Server update in progress, terminals reconnect briefly', action: isAdmin }
  }
  if (isAdmin && behind && serverLatest && (flow.name === 'failed' || dismissed.server !== serverLatest)) {
    return { text: `Server ${bareVersion(serverLatest)} is available`, action: true }
  }
  if (serves && cliVersion && shellIsStale(cliVersion) && dismissed.shell !== cliVersion) {
    return { text: 'The desktop app is out of date', action: true }
  }
  return null
}

const noneDismissed: Record<UpdateKind, string> = { cli: '', server: '', shell: '' }

function ShellBanner() {
  const cliVersion = useStore((s) => s.capabilities?.version)
  const dismissed = useStore((s) => s.dismissedUpdates.shell)
  const buildError = useStore((s) => s.update?.shell_build_error)
  if (!shellIsStale(cliVersion) || !cliVersion) return null
  if (dismissed === cliVersion) return null
  return (
    <div role="status" className={banner}>
      <div aria-hidden className={bannerIcon}>
        <MonitorCog className="size-4" />
      </div>
      <div className={bannerContent}>
        <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
          <p className="font-medium">The desktop app is out of date.</p>
          <p className="text-xs text-muted-foreground">
            Built by aether {desktopBridge()?.shellVersion}; serving {cliVersion}.
          </p>
        </div>
        <p className="text-xs text-muted-foreground">
          The dashboard itself is current - it ships inside the CLI. Only the
          window around it is old.
        </p>
        {buildError && (
          <div className="space-y-1">
            <p className="text-xs font-medium text-state-failed">The last rebuild failed:</p>
            <p className={cn(verbatim, 'text-state-failed')}>{buildError}</p>
          </div>
        )}
        <Collapsible className="text-xs text-muted-foreground">
          <CollapsibleTrigger className="font-medium hover:text-foreground">
            Rebuild instructions
          </CollapsibleTrigger>
          <CollapsibleContent>
            <CopyableCommand command="aether gui build" />
          </CollapsibleContent>
        </Collapsible>
      </div>
      <Dismiss kind="shell" version={cliVersion} />
    </div>
  )
}

/** A progress frame beats the fetched status because it is newer. */
type ServerFlow =
  | { name: 'available' }
  | { name: 'scheduled'; version: string; by: string }
  | { name: 'applying' }
  | { name: 'restarting' }
  | { name: 'failed'; detail: string }

function serverFlow(
  status: ServerUpdateStatus | null,
  progress: ServerUpdatePayload | null,
): ServerFlow {
  switch (progress?.phase) {
    case 'scheduled':
      return {
        name: 'scheduled',
        version: progress.version ?? '',
        by: progress.actor_id ?? '',
      }
    case 'applying':
      return { name: 'applying' }
    case 'restarting':
      return { name: 'restarting' }
    case 'failed':
      return { name: 'failed', detail: progress.detail ?? '' }
    case 'cancelled':
      return { name: 'available' }
  }
  if (status?.pending) {
    return {
      name: 'scheduled',
      version: status.pending.version,
      by: status.pending.requested_by,
    }
  }
  return { name: 'available' }
}

/** Mirrors the server's idle check in internal/scheduler. */
function activeRunCount(
  runs: Record<string, RunRecord>,
  paused: Record<string, boolean>,
): number {
  return Object.values(runs).filter(
    (r) =>
      (r.status === 'queued' || r.status === 'provisioning' || r.status === 'running') &&
      !paused[r.id],
  ).length
}

function noButtonsLine(
  status: ServerUpdateStatus | null,
  error: string | null,
): string {
  if (status) {
    const reason = status.incapable ? `: ${status.incapable}` : ''
    return `The server cannot update itself${reason}. Run these on the server host:`
  }
  if (error) {
    return `The dashboard could not read the server's update status: ${error}. Run these on the server host:`
  }
  return 'The dashboard cannot update the server. Run these on the server host:'
}

/** An open workspace shell holds an update back too: it has no container to reattach to. */
function waitingLine(waiting: ServerUpdateWaiting | undefined): string {
  if (!waiting) return ''
  const parts: string[] = []
  if (waiting.runs > 0) {
    parts.push(`${waiting.runs} ${waiting.runs === 1 ? 'run' : 'runs'}`)
  }
  if (waiting.shells > 0) {
    parts.push(`${waiting.shells} open ${waiting.shells === 1 ? 'shell' : 'shells'}`)
  }
  return parts.length ? `Waiting for ${parts.join(' and ')}.` : ''
}

function confirmLine(active: number): string {
  if (active === 0) {
    return 'No runs are active right now. Attached terminals reconnect on their own.'
  }
  const runs = active === 1 ? '1 run is' : `${active} runs are`
  return `${runs} active right now. They keep running: the server reattaches to their containers when it comes back, and attached terminals reconnect on their own.`
}

function manualCommands(status: ServerUpdateStatus | null): string[] {
  return status?.manual_commands?.length
    ? status.manual_commands
    : ['sudo aether update', 'sudo systemctl restart aether-server']
}

/** The local gateway advertises every method to anyone, so the admin role is checked too. */
function ServerBanner({ client, onRetry }: { client: Api; onRetry: () => void }) {
  const update = useStore((s) => s.update)
  const status = useStore((s) => s.serverUpdate)
  const statusError = useStore((s) => s.serverUpdateError)
  const progress = useStore((s) => s.serverUpdateProgress)
  const applyProgress = useStore((s) => s.applyServerUpdate)
  const members = useStore((s) => s.members)
  const runs = useStore((s) => s.runs)
  const pausedRuns = useStore((s) => s.pausedRuns)
  const dismissed = useStore((s) => s.dismissedUpdates.server)
  const isAdmin = useIsAdmin()
  const canUpdate = useCapability().hasMethod('server.update')
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // update.check is read once, so only the status call clears this after a restart.
  const behind = status ? status.update_available : (update?.server_behind ?? false)
  const latest = status?.latest || update?.cli.latest || ''
  const running = status?.server_version || update?.server_version || ''
  const flow = serverFlow(status, progress)

  if (!isAdmin) return null
  if (!behind || !latest) return null
  if (dismissed === latest && flow.name === 'available') return null

  const act = async (when: ServerUpdateWhen) => {
    setError(null)
    setBusy(true)
    try {
      const result = await client.serverUpdate(when)
      // The feed reaches workspace timelines only, so a server with no workspaces sends nothing else.
      applyProgress({
        phase: result.status,
        version: result.version,
        actor_id: result.requested_by,
      })
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  const capable = status?.capable ?? false
  const active = activeRunCount(runs, pausedRuns)
  const waiting = waitingLine(status?.waiting)
  const scheduledBy =
    flow.name === 'scheduled' ? (members[flow.by]?.display_name ?? flow.by) : ''

  const NoticeIcon = flow.name === 'failed' || error ? CircleAlert : ServerCog

  return (
    <div role="status" className={banner}>
      <div aria-hidden className={bannerIcon}>
        <NoticeIcon className="size-4" />
      </div>
      <div className={bannerContent}>
        <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
          <p className="font-medium">The server is behind.</p>
          <p className="text-xs text-muted-foreground">
            Server {running}, latest {latest}.
          </p>
        </div>
        {flow.name === 'available' && capable && (
          <Collapsible className="text-xs text-muted-foreground">
            <CollapsibleTrigger className="font-medium hover:text-foreground">
              What a server restart affects
            </CollapsibleTrigger>
            <CollapsibleContent>
              <p className="mt-1.5 leading-5">
                Updating replaces the server binaries and restarts the server. Runs
                keep going - the server reattaches to their containers when it comes
                back - and attached terminals reconnect on their own.
              </p>
            </CollapsibleContent>
          </Collapsible>
        )}
        {flow.name === 'scheduled' && (
          <>
            <p className="text-muted-foreground">
              Update to {flow.version || latest} scheduled by {scheduledBy}, applies
              when no run is active.
            </p>
            {waiting && <p className="text-muted-foreground">{waiting}</p>}
          </>
        )}
        {flow.name === 'applying' && (
          <p className="text-muted-foreground">
            Downloading and verifying the release. Nothing has been replaced yet.
          </p>
        )}
        {flow.name === 'restarting' && (
          <p className="text-muted-foreground">
            Restarting on the new version. Attached terminals reconnect on their
            own.
          </p>
        )}
        {flow.name === 'failed' && (
          <>
            <p className="text-muted-foreground">
              The update failed and nothing was replaced.
            </p>
            <p className={cn(verbatim, 'text-state-failed')}>{flow.detail}</p>
          </>
        )}
        {error && <p className={cn(verbatim, 'text-state-failed')}>{error}</p>}
        {(!capable || flow.name === 'failed') && (
          <>
            <p className="text-muted-foreground">
              {capable
                ? 'Run these on the server host instead:'
                : noButtonsLine(status, statusError)}
            </p>
            <div className="space-y-1">
              {manualCommands(status).map((command) => (
                <CopyableCommand key={command} command={command} />
              ))}
            </div>
          </>
        )}
      </div>
      {!status && statusError && (
        <div className={bannerActions}>
          <Button size="sm" variant="secondary" onClick={onRetry}>
            Retry
          </Button>
        </div>
      )}
      {canUpdate && capable && (
        <div className={bannerActions}>
          {flow.name === 'scheduled' ? (
            <Button
              size="sm"
              variant="secondary"
              disabled={busy}
              onClick={() => void act('cancel')}
            >
              Cancel
            </Button>
          ) : (
            (flow.name === 'available' || flow.name === 'failed') && (
              <>
                <Button size="sm" disabled={busy} onClick={() => setConfirming(true)}>
                  Update now
                </Button>
                <Button
                  size="sm"
                  variant="secondary"
                  disabled={busy}
                  onClick={() => void act('idle')}
                >
                  Update when idle
                </Button>
              </>
            )
          )}
        </div>
      )}
      <Dismiss kind="server" version={latest} />
      <Dialog open={confirming} onOpenChange={setConfirming}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Update the server to {latest}?</DialogTitle>
            <DialogDescription>{confirmLine(active)}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="secondary" onClick={() => setConfirming(false)}>
              Keep waiting
            </Button>
            <Button
              onClick={() => {
                setConfirming(false)
                void act('now')
              }}
            >
              Update and restart
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
