// The two update prompts, above everything the shell renders: the CLI on
// this machine is behind, or the server it talks to is. The CLI half comes
// from one `update.check` call to the local gateway; the server half comes
// from `server.update_status`, which any member may read and which says
// whether the server can replace its own binaries. A server that cannot -
// the documented unprivileged install - still gets the two commands to run
// on its host rather than a button that could not work.

import { X } from 'lucide-react'
import { useEffect, useState } from 'react'
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
import { api, type Api } from '@/lib/api'
import { bareVersion, message } from '@/lib/format'
import type {
  ServerUpdatePayload,
  ServerUpdateStatus,
  ServerUpdateWhen,
  UpdateApplyResult,
  UpdateBuildStatus,
  UpdateStatus,
} from '@/lib/types'
import { useStore } from '@/store'
import { useCapability, useIsAdmin } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import type { UpdateKind } from '@/store/ui'

const banner =
  'flex flex-wrap items-start gap-x-3 gap-y-1 border-b bg-card px-3 py-2 text-sm'

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

/** The dismiss control both banners carry. */
function Dismiss({ kind, version }: { kind: UpdateKind; version: string }) {
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
  // Re-read whenever server.info names a different version: that is the
  // signal the restart is over, and the fresh status is what makes the
  // banner disappear once the server is current.
  const serverVersion = useStore((s) => s.info?.server_version)

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
      // A server too old to serve the method, or one that could not reach
      // the release feed, leaves the older banner in place; the failure is
      // not worth a prompt of its own.
      .catch(() => {})
    return () => {
      live = false
    }
  }, [readsServerUpdate, serverVersion, client, setServerUpdate])

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
      <ServerBanner client={client} />
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

type ApplyState =
  | { name: 'idle' }
  | { name: 'applying' }
  | { name: 'done'; result: UpdateApplyResult }
  | { name: 'failed'; detail: string }

/**
 * What the button, and everything above it, show. Derived from the apply
 * result and the update.status polls that follow it when the gateway
 * started a desktop-app rebuild - never stored directly, so the two never
 * drift out of sync with each other.
 */
type Flow =
  | { name: 'idle' }
  | { name: 'applying' }
  | { name: 'applyFailed'; detail: string }
  // update.apply answered and there is no rebuild to wait on: today's flow,
  // unchanged.
  | { name: 'applied'; result: UpdateApplyResult }
  | { name: 'rebuilding'; result: UpdateApplyResult; phase?: string }
  // The rebuild finished on a gateway that is not going away - a browser
  // tab - so the app on disk is new and only the window is old.
  | { name: 'rebuilt'; result: UpdateApplyResult }
  | { name: 'relaunching' }
  | { name: 'rebuildFailed'; error: string }

function deriveFlow(apply: ApplyState, build: UpdateBuildStatus | null): Flow {
  if (apply.name === 'idle') return { name: 'idle' }
  if (apply.name === 'applying') return { name: 'applying' }
  if (apply.name === 'failed') return { name: 'applyFailed', detail: apply.detail }
  const { result } = apply
  if (!result.rebuilding) return { name: 'applied', result }
  if (build?.phase === 'error') {
    return { name: 'rebuildFailed', error: build.error ?? 'the rebuild failed' }
  }
  if (build?.phase === 'done') {
    return result.restarting ? { name: 'relaunching' } : { name: 'rebuilt', result }
  }
  return { name: 'rebuilding', result, phase: build?.phase }
}

/**
 * What the update replaced and what is left to do. The paths are named
 * because a single-box install swaps `aether-server` beside the CLI, and
 * that server keeps running the old code until its unit is restarted - so
 * the command the gateway sends back is shown rather than left to the
 * operator to remember.
 */
function Applied({ result, note }: { result: UpdateApplyResult; note?: string }) {
  // The gateway's note describes what it was about to do. Once that work
  // is over the caller passes what actually happened instead, so the
  // banner does not keep saying a finished rebuild is still running.
  const trailing =
    note ?? (result.restarting ? 'Restarting the dashboard.' : result.note ?? '')
  return (
    <div className="space-y-1 text-muted-foreground">
      <p>
        Updated to {result.version}.{trailing ? ` ${trailing}` : ''}
      </p>
      <p className="font-mono text-[11px]">{result.updated.join(', ')}</p>
      {result.restart_command && (
        <>
          <p>The server binary beside it was replaced too. Restart the unit:</p>
          <CopyableCommand command={result.restart_command} />
        </>
      )}
    </div>
  )
}

/**
 * The CLI is behind. Every member sees this: the binary is on their own
 * machine, so no role gates it.
 */
function CliBanner({ update, client }: { update: UpdateStatus; client: Api }) {
  const latest = update.cli.latest ?? ''
  const dismissed = useStore((s) => s.dismissedUpdates.cli)
  const setGatewayRestarting = useStore((s) => s.setGatewayRestarting)
  const [apply, setApply] = useState<ApplyState>({ name: 'idle' })
  const [build, setBuild] = useState<UpdateBuildStatus | null>(null)

  // Polls update.status while a rebuild the apply started is in flight.
  // Self-contained: it starts once `apply` becomes the rebuilding `done`
  // state and stops itself, from inside the tick that sees a terminal
  // phase, without depending on `build` and thereby restarting the clock
  // on every poll.
  useEffect(() => {
    if (apply.name !== 'done') return
    if (!apply.result.rebuilding) return
    let cancelled = false
    const timer = setInterval(() => {
      void (async () => {
        try {
          const status = await client.localUpdateStatus()
          if (cancelled) return
          setBuild(status)
          if (status.phase === 'done' || status.phase === 'error') {
            clearInterval(timer)
          }
        } catch {
          // A failed poll is transient; the next tick retries.
        }
      })()
    }, 1000)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [apply, client])

  if (!update.cli.update_available || !latest || dismissed === latest) return null

  const run = async () => {
    setApply({ name: 'applying' })
    setBuild(null)
    try {
      const result = await client.localUpdateApply()
      // The page must stop showing the connection-error screen while the
      // gateway is deliberately going away. `restarting` is exactly that
      // claim; a rebuild on an unsupervised gateway is not - that tab keeps
      // its gateway, and suppressing the page there would hide a real
      // disconnect later, because the flag is never cleared.
      if (result.restarting) setGatewayRestarting(true)
      setApply({ name: 'done', result })
    } catch (err) {
      // The gateway's own message, verbatim: it names the directory and the
      // exact sudo command when the binary is not writable.
      setApply({ name: 'failed', detail: message(err) })
    }
  }

  const flow = deriveFlow(apply, build)
  // Nothing is left for the button to do once the CLI is swapped: a second
  // apply would only answer that this is already the newest release, and a
  // failed rebuild is fixed by the command the banner already shows.
  const offerButton =
    update.cli.can_self_update &&
    flow.name !== 'applied' &&
    flow.name !== 'rebuilt' &&
    flow.name !== 'rebuildFailed'
  const busy =
    flow.name === 'applying' || flow.name === 'rebuilding' || flow.name === 'relaunching'
  const buttonLabel =
    flow.name === 'applying'
      ? 'Updating...'
      : flow.name === 'rebuilding'
        ? 'Rebuilding...'
        : flow.name === 'relaunching'
          ? 'Relaunching...'
          : 'Update now'

  return (
    <div role="status" className={banner}>
      <div className="min-w-0 space-y-1">
        <p>
          <span className="font-medium">Aether {latest} is available.</span> You
          are running {update.cli.version}.
        </p>
        {update.cli.can_self_update ? (
          <p className="text-muted-foreground">
            Updating replaces the aether binary on this machine and restarts
            the dashboard. Attached terminals and any running file sync stop
            with it; the runs themselves keep going on the server.
          </p>
        ) : (
          <p className="text-muted-foreground">
            Self-update is not supported on Windows. Download {latest} from the
            release page and replace the binary yourself.
          </p>
        )}
        {flow.name === 'applying' && (
          <p className="text-muted-foreground">Updating the CLI...</p>
        )}
        {flow.name === 'applied' && <Applied result={flow.result} />}
        {flow.name === 'rebuilt' && (
          <Applied
            result={flow.result}
            note="The app was rebuilt; restart it to use the new version."
          />
        )}
        {flow.name === 'applyFailed' && (
          <p className="font-mono text-xs text-state-failed">{flow.detail}</p>
        )}
        {flow.name === 'rebuilding' && (
          <div className="text-muted-foreground">
            <p>
              Rebuilding the app (about a minute; the first time also fetches
              Node)...
            </p>
            {flow.phase && <p className="font-mono text-[11px]">{flow.phase}</p>}
          </div>
        )}
        {flow.name === 'relaunching' && (
          <p className="text-muted-foreground">Relaunching</p>
        )}
        {flow.name === 'rebuildFailed' && (
          <div className="space-y-1">
            <p className="font-mono text-xs text-state-failed">{flow.error}</p>
            <p className="text-muted-foreground">Rebuild it yourself:</p>
            <CopyableCommand command="aether gui build" />
          </div>
        )}
      </div>
      <div className="flex items-center gap-2">
        {offerButton && (
          <Button size="sm" disabled={busy} onClick={() => void run()}>
            {buttonLabel}
          </Button>
        )}
        {update.cli.release_url && (
          <a
            href={update.cli.release_url}
            target="_blank"
            rel="noreferrer"
            className="text-xs underline underline-offset-2 hover:text-foreground"
          >
            Release notes
          </a>
        )}
      </div>
      <Dismiss kind="cli" version={latest} />
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
 * Why the buttons are missing. The server's own reason is kept verbatim -
 * on the documented unprivileged install it names the binary directory -
 * because that is the part an admin acts on.
 */
function incapableLine(status: ServerUpdateStatus | null): string {
  const reason = status?.incapable ? `: ${status.incapable}` : ''
  return `The server cannot update itself${reason}. Run these on the server host:`
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
function ServerBanner({ client }: { client: Api }) {
  const update = useStore((s) => s.update)
  const status = useStore((s) => s.serverUpdate)
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
          <p className="text-muted-foreground">
            Update to {flow.version || latest} scheduled by {scheduledBy}, applies
            when no run is active.
          </p>
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
              {capable ? 'Run these on the server host instead:' : incapableLine(status)}
            </p>
            <div className="space-y-1">
              {manualCommands(status).map((command) => (
                <CopyableCommand key={command} command={command} />
              ))}
            </div>
          </>
        )}
      </div>
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
            <DialogDescription>
              {active === 1 ? '1 run is' : `${active} runs are`} active right now.
              They keep running: the server reattaches to their containers when it
              comes back, and attached terminals reconnect on their own.
            </DialogDescription>
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
