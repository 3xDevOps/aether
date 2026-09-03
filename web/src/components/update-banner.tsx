// The two update prompts, above everything the shell renders. Both are
// answers to one `update.check` call: the CLI on this machine is behind, or
// the server it talks to is. Only the first one can be acted on from here -
// the dashboard has no way to write to the server host - so the second says
// exactly what to run there instead of offering a button that cannot work.

import { X } from 'lucide-react'
import { useEffect, useState } from 'react'
import { CopyableCommand } from '@/components/copyable-command'
import { desktopBridge } from '@/components/shell/title-bar'
import { Button } from '@/components/ui/button'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { UpdateApplyResult, UpdateBuildStatus, UpdateStatus } from '@/lib/types'
import { useStore } from '@/store'
import { useCapability, useIsAdmin } from '@/store/hooks'
import type { UpdateKind } from '@/store/ui'

const banner =
  'flex flex-wrap items-start gap-x-3 gap-y-1 border-b bg-card px-3 py-2 text-sm'

/** Release tags arrive as "v1.2.3", the shell's version as "1.2.3". */
function bare(version: string): string {
  return version.replace(/^v/, '')
}

/**
 * Whether the desktop app was built by a different CLI than the one serving
 * this gateway. Both sides have to be known: a browser tab has no shell at
 * all, and a gateway that predates the capabilities field reports no
 * version, and neither of those is a mismatch.
 */
function shellIsStale(cliVersion: string | undefined): boolean {
  const shell = desktopBridge()?.shellVersion
  if (!shell || !cliVersion) return false
  return bare(shell) !== bare(cliVersion)
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
 * Both banners, and the one `update.check` they read. Renders nothing where
 * the gateway does not serve the verb: only the desktop gateway can update
 * the binary beside it, so a remote monitor has nothing to say here.
 */
export function UpdateBanners({ client = api }: { client?: Api } = {}) {
  const serves = useCapability().hasLocal('update.check')
  const update = useStore((s) => s.update)
  const setUpdate = useStore((s) => s.setUpdate)

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

  return (
    <>
      {/* Independent of the release check: the shell goes stale the moment
          the CLI it was built by is replaced, which is the flow this
          notice exists for and the one where no update is available any
          more. */}
      <ShellBanner />
      {serves && update && <CliBanner update={update} client={client} />}
      {serves && update && <ServerBanner update={update} />}
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
    return result.restarting ? { name: 'relaunching' } : { name: 'applied', result }
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
function Applied({ result }: { result: UpdateApplyResult }) {
  return (
    <div className="space-y-1 text-muted-foreground">
      <p>
        Updated to {result.version}.
        {result.restarting ? ' Restarting the dashboard.' : result.note ? ` ${result.note}` : ''}
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
      // The page must stop showing the connection-error screen the moment
      // the gateway says it is going away, whether that is the respawn
      // itself or the rebuild that precedes it - never cleared, since from
      // here the page is on its way out regardless of how it reconnects.
      if (result.restarting || result.rebuilding) setGatewayRestarting(true)
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
    update.cli.can_self_update && flow.name !== 'applied' && flow.name !== 'rebuildFailed'
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
 * The server is behind. Admins only: capability says the gateway can carry
 * the check, the role says whether this member is the one who runs commands
 * on the server host. The dashboard cannot update the server at all - the
 * binary is on another machine, behind SSH - so this names the two commands
 * rather than pretending to a button.
 */
function ServerBanner({ update }: { update: UpdateStatus }) {
  const latest = update.cli.latest ?? ''
  const isAdmin = useIsAdmin()
  const dismissed = useStore((s) => s.dismissedUpdates.server)

  if (!update.server_behind || !latest) return null
  if (!isAdmin) return null
  if (dismissed === latest) return null

  return (
    <div role="status" className={banner}>
      <div className="min-w-0 space-y-1">
        <p>
          <span className="font-medium">The server is behind.</span> Server{' '}
          {update.server_version}, latest {latest}.
        </p>
        <p className="text-muted-foreground">
          The dashboard cannot update the server. Run these on the server host:
        </p>
        <div className="space-y-1">
          <CopyableCommand command="sudo aether update" />
          <CopyableCommand command="sudo systemctl restart aether-server" />
        </div>
      </div>
      <Dismiss kind="server" version={latest} />
    </div>
  )
}
