// The CLI-is-behind prompt. One `update.check` call to the local gateway
// says whether a release is out and how `update.apply` would get to write
// the binary: straight into a writable directory, through the macOS
// administrator password dialog, or not at all - in which case the banner
// hands over the sudo command rather than a button that could not work.

import { useEffect, useState } from 'react'
import { CopyableCommand } from '@/components/copyable-command'
import { Button } from '@/components/ui/button'
import { banner, Dismiss } from '@/components/update-banner-shared'
import { ApiError, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { UpdateApplyResult, UpdateBuildStatus, UpdateStatus } from '@/lib/types'
import { useStore } from '@/store'

type ApplyState =
  | { name: 'idle' }
  | { name: 'applying' }
  | { name: 'done'; result: UpdateApplyResult }
  | { name: 'failed'; detail: string }
  // The member closed the administrator dialog, or macOS refused the
  // password. Not a failure: nothing was downloaded into place.
  | { name: 'cancelled'; detail: string }

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
  | { name: 'applyCancelled'; detail: string }
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
  if (apply.name === 'cancelled') {
    return { name: 'applyCancelled', detail: apply.detail }
  }
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
 * What the member is told before pressing anything. The gateway's probe
 * says how the binary gets written; a probe that failed leaves both fields
 * absent and the banner then reads as it did before the probe existed.
 */
function HowItInstalls({ update }: { update: UpdateStatus }) {
  const { cli, cli_path: path, install_method: method } = update
  if (!cli.can_self_update) {
    return (
      <p className="text-muted-foreground">
        Self-update is not supported on Windows. Download {cli.latest} from the
        release page and replace the binary yourself.
      </p>
    )
  }
  if (method === 'manual') {
    return (
      <>
        <p className="text-muted-foreground">
          {path} is not writable by this account. Update it from a terminal:
        </p>
        <CopyableCommand command="sudo aether update" />
      </>
    )
  }
  return (
    <>
      <p className="text-muted-foreground">
        Updating replaces the aether binary on this machine and restarts the
        dashboard. Attached terminals and any running file sync stop with it;
        the runs themselves keep going on the server.
      </p>
      {method === 'admin-prompt' && (
        // The dialog carries osascript's name, not Aether's, and a member
        // who has never heard of osascript would rightly refuse it.
        <p className="text-muted-foreground">
          macOS will ask for an administrator password: {path} is in a
          directory this account cannot write to. The dialog is labelled
          osascript, the tool Aether asks through. Aether never sees your
          password.
        </p>
      )}
    </>
  )
}

/**
 * The CLI is behind. Every member sees this: the binary is on their own
 * machine, so no role gates it.
 */
export function CliBanner({ update, client }: { update: UpdateStatus; client: Api }) {
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
      // 403 is the gateway's word for "administrator access was not
      // granted": the member closed the dialog or macOS refused the
      // password. A bearer-token refusal is 401, so nothing else on this
      // verb answers 403. Every other failure keeps the gateway's own
      // message, verbatim: it names the directory and the exact sudo
      // command when the binary is not writable.
      if (err instanceof ApiError && err.status === 403) {
        setApply({ name: 'cancelled', detail: message(err) })
      } else {
        setApply({ name: 'failed', detail: message(err) })
      }
    }
  }

  const flow = deriveFlow(apply, build)
  // Nothing is left for the button to do once the CLI is swapped: a second
  // apply would only answer that this is already the newest release, and a
  // failed rebuild is fixed by the command the banner already shows. A
  // binary the gateway cannot write never gets a button at all.
  const offerButton =
    update.cli.can_self_update &&
    update.install_method !== 'manual' &&
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
  const applyingLine =
    update.install_method === 'admin-prompt'
      ? `Downloading ${latest}, then macOS asks for an administrator password...`
      : 'Updating the CLI...'

  return (
    <div role="status" className={banner}>
      <div className="min-w-0 space-y-1">
        <p>
          <span className="font-medium">Aether {latest} is available.</span> You
          are running {update.cli.version}.
        </p>
        <HowItInstalls update={update} />
        {flow.name === 'applying' && (
          <p className="text-muted-foreground">{applyingLine}</p>
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
        {flow.name === 'applyCancelled' && (
          <>
            <p className="text-muted-foreground">Update cancelled, nothing was changed.</p>
            <p className="font-mono text-xs text-muted-foreground">{flow.detail}</p>
          </>
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
