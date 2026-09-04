// The update prompts, above everything the shell renders: the CLI on this
// machine is behind (cli-update-banner.tsx), the desktop app around the
// dashboard is stale, or the server it talks to is behind. The CLI half
// comes from one `update.check` call to the local gateway; the server half
// comes from `server.update_status`, which any member may read and which
// says whether the server can replace its own binaries. A server that
// cannot - the documented unprivileged install - still gets the two
// commands to run on its host rather than a button that could not work.

import { useEffect, useState } from 'react'
import { CliBanner } from '@/components/cli-update-banner'
import { CopyableCommand } from '@/components/copyable-command'
import { desktopBridge } from '@/components/shell/title-bar'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { banner, Dismiss } from '@/components/update-banner-shared'
import { api, type Api } from '@/lib/api'
import { bareVersion, message } from '@/lib/format'
import type {
  ServerUpdatePayload,
  ServerUpdateStatus,
  ServerUpdateWaiting,
  ServerUpdateWhen,
} from '@/lib/types'
import { useStore } from '@/store'
import { useCapability, useIsAdmin } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

/**
 * Whether the desktop app was built by a different CLI than the one serving
 * this gateway. Both sides have to be known: a browser tab has no shell at
 * all, and a gateway that predates the capabilities field reports no
 * version, and neither of those is a mismatch.
 */
function shellIsStale(cliVersion: string | undefined): boolean {
  const shell = desktopBridge()?.shellVersion
  if (!shell || !cliVersion) return false
  return bareVersion(shell) !== bareVersion(cliVersion)
}

/**
 * The three banners and the two reads behind them. The CLI and shell
 * prompts need `update.check`, which only the desktop gateway serves - a
 * remote monitor cannot update a binary on your machine - while the server
 * prompt rides `server.update_status` and shows wherever the member is an
 * admin.
 */
export function UpdateBanners({ client = api }: { client?: Api } = {}) {
  const caps = useCapability()
  const serves = caps.hasLocal('update.check')
  const readsServerUpdate = caps.hasMethod('server.update_status')
  const update = useStore((s) => s.update)
  const setUpdate = useStore((s) => s.setUpdate)
  const setServerUpdate = useStore((s) => s.setServerUpdate)
  const setServerUpdateFailed = useStore((s) => s.setServerUpdateFailed)
  // Re-read whenever the connection changes state or server.info names a
  // different version. The reconnect is the one that matters: a server
  // that updates itself re-executes, the socket drops, and the fresh
  // status is what ends the banner and the status bar's notice. A read
  // that failed is retried by the same rule, plus the banner's Retry.
  const serverVersion = useStore((s) => s.info?.server_version)
  const connection = useStore((s) => s.connection)
  const [statusReads, setStatusReads] = useState(0)

  useEffect(() => {
    if (!serves) return
    let live = true
    void client
      .localUpdateCheck()
      .then((status) => {
        if (live) setUpdate(status)
      })
      // A failed check is not worth a banner of its own: the release lookup
      // is a network read the member did not ask for.
      .catch(() => {})
    return () => {
      live = false
    }
  }, [serves, client, setUpdate])

  useEffect(() => {
    if (!readsServerUpdate || !serverVersion) return
    let live = true
    void client
      .serverUpdateStatus()
      .then((status) => {
        if (live) setServerUpdate(status)
      })
      .catch((err) => {
        // Recorded rather than swallowed: the banner has to say it could
        // not read the status instead of claiming the server cannot
        // update itself, which is a different thing entirely.
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

  return (
    <>
      {/* Independent of the release check: the shell goes stale the moment
          the CLI it was built by is replaced, which is the flow this
          notice exists for and the one where no update is available any
          more. */}
      <ShellBanner />
      {serves && update && <CliBanner update={update} client={client} />}
      {/* Not gated on `update.check`: the server answers for itself, so an
          admin on any gateway can act on it. */}
      <ServerBanner client={client} onRetry={() => setStatusReads((n) => n + 1)} />
    </>
  )
}

/**
 * The desktop app was built by a different CLI than the one serving this
 * gateway. Almost always because an update just replaced the CLI: the SPA
 * ships inside it and is already new, the Electron shell around it is not.
 * A browser tab has no shell and renders nothing.
 */
function ShellBanner() {
  const cliVersion = useStore((s) => s.capabilities?.version)
  const dismissed = useStore((s) => s.dismissedUpdates.shell)
  // Not the check this banner is keyed on - shellIsStale only needs the
  // shell and CLI versions - but the last in-app rebuild may have failed,
  // which is worth saying here since it is why the shell is still old.
  const buildError = useStore((s) => s.update?.shell_build_error)

  if (!shellIsStale(cliVersion) || !cliVersion) return null
  if (dismissed === cliVersion) return null

  return (
    <div role="status" className={banner}>
      <div className="min-w-0 space-y-1">
        <p>
          <span className="font-medium">The desktop app is out of date.</span>{' '}
          It was built by aether {desktopBridge()?.shellVersion}, and {cliVersion}{' '}
          is serving it now.
        </p>
        <p className="text-muted-foreground">
          The dashboard itself is current - it ships inside the CLI. Only the
          window around it is old.
        </p>
        {buildError && (
          <>
            <p className="text-muted-foreground">The last rebuild failed:</p>
            <p className="font-mono text-xs text-state-failed">{buildError}</p>
          </>
        )}
        <p className="text-muted-foreground">Rebuild it in a terminal:</p>
        <CopyableCommand command="aether gui build" />
      </div>
      <Dismiss kind="shell" version={cliVersion} />
    </div>
  )
}

/**
 * How far the running server update has got, from the two sources that
 * know: the `server.update` feed, and the pending update the status call
 * reports. A cancel is not a state of its own - the update it cleared is
 * simply gone - and a progress frame always beats the fetched status,
 * because it is newer.
 */
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

/**
 * How many runs are working in the scope this member can see. It mirrors
 * the server's own idle check (internal/scheduler): a run parked at
 * needs-attention is waiting on a person and a paused run is a frozen
 * container, so neither has anything running inside it to interrupt.
 */
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

/**
 * Why the buttons are missing, in the terms the dashboard can defend.
 * Three different things end up here and only one of them is the server
 * saying it cannot update itself: the status read may have failed, and a
 * gateway may not carry the method at all. Claiming the first for either
 * of the others would be a friendlier sentence that is not true.
 */
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

/**
 * What a pending update is still waiting for. The scheduled line says "no
 * run is active", but an open workspace shell holds an update back too -
 * it has no container to reattach to - and an admin whose update never
 * fires needs to see that here rather than in `aether server update
 * --status`. Paused runs are left out: they hold nothing back.
 */
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

/** What the confirm dialog says a restart costs, with the live run count. */
function confirmLine(active: number): string {
  if (active === 0) {
    return 'No runs are active right now. Attached terminals reconnect on their own.'
  }
  const runs = active === 1 ? '1 run is' : `${active} runs are`
  return `${runs} active right now. They keep running: the server reattaches to their containers when it comes back, and attached terminals reconnect on their own.`
}

/** The commands to run on a server that cannot update itself. */
function manualCommands(status: ServerUpdateStatus | null): string[] {
  return status?.manual_commands?.length
    ? status.manual_commands
    : ['sudo aether update', 'sudo systemctl restart aether-server']
}

/**
 * The server is behind. Admins only: `server.update` is an admin method,
 * and the local gateway advertises every method regardless of who is
 * behind it, so the capability is half the gate and the caller's role is
 * the other half.
 *
 * A server that serves `server.update_status` says whether it can replace
 * its own binaries. When it can, this is a pair of buttons; when it cannot
 * - the documented unprivileged install - it is the two commands to run on
 * the server host, as it has always been. A server too old to answer the
 * method at all keeps that older banner too.
 */
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

  // The status call is the authority once the server answers it: after the
  // restart it reports the new version, which is how this banner goes away
  // on its own. `update.check` is the fallback for a server too old to
  // serve it, and it is fetched once, so it cannot answer that question.
  const behind = status ? status.update_available : (update?.server_behind ?? false)
  const latest = status?.latest || update?.cli.latest || ''
  const running = status?.server_version || update?.server_version || ''
  const flow = serverFlow(status, progress)

  if (!isAdmin) return null
  if (!behind || !latest) return null
  // Dismissing silences the offer, not an update that is already moving:
  // the phases are what say why the server is about to restart.
  if (dismissed === latest && flow.name === 'available') return null

  const act = async (when: ServerUpdateWhen) => {
    setError(null)
    setBusy(true)
    try {
      const result = await client.serverUpdate(when)
      // The server publishes the same phase to the feed, but only into a
      // workspace timeline: a server with no workspaces yet has nowhere to
      // put it, and the result is then the only thing that arrives.
      applyProgress({
        phase: result.status,
        version: result.version,
        actor_id: result.requested_by,
      })
    } catch (err) {
      // The server's own refusal, verbatim: an incapable server names the
      // commands to run on its host inside that message.
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  const capable = status?.capable ?? false
  const active = activeRunCount(runs, pausedRuns)
  const waiting = waitingLine(status?.waiting)
  // The feed and the pending row both name the member by id; a member the
  // store has never seen falls back to that id rather than to nobody.
  const scheduledBy =
    flow.name === 'scheduled' ? (members[flow.by]?.display_name ?? flow.by) : ''

  return (
    <div role="status" className={banner}>
      <div className="min-w-0 space-y-1">
        <p>
          <span className="font-medium">The server is behind.</span> Server{' '}
          {running}, latest {latest}.
        </p>
        {flow.name === 'available' && capable && (
          <p className="text-muted-foreground">
            Updating replaces the server binaries and restarts the server. Runs
            keep going - the server reattaches to their containers when it comes
            back - and attached terminals reconnect on their own.
          </p>
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
            <p className="font-mono text-xs text-state-failed">{flow.detail}</p>
          </>
        )}
        {error && <p className="font-mono text-xs text-state-failed">{error}</p>}
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
        <div className="flex items-center gap-2">
          <Button size="sm" variant="outline" onClick={onRetry}>
            Retry
          </Button>
        </div>
      )}
      {canUpdate && capable && (
        <div className="flex items-center gap-2">
          {flow.name === 'scheduled' ? (
            <Button
              size="sm"
              variant="outline"
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
                  variant="outline"
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
            <Button variant="outline" onClick={() => setConfirming(false)}>
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
