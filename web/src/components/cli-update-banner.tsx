// The CLI-is-behind prompt. `update.check` on the local gateway says
// whether a release is out and how `update.apply` would get to write
// the binary: straight into a writable directory, through the macOS
// administrator password dialog, or not at all - in which case the banner
// hands over the sudo command rather than a button that could not work.

import { useEffect, useState } from 'react'
import { AlertTriangle, CheckCircle2, Download, LoaderCircle } from 'lucide-react'
import { CopyableCommand } from '@/components/copyable-command'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import {
  banner,
  bannerActions,
  bannerContent,
  bannerIcon,
  Dismiss,
  verbatim,
} from '@/components/update-banner-shared'
import { ApiError, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { UpdateApplyResult, UpdateBuildStatus, UpdateStatus } from '@/lib/types'
import { cn, focusRing } from '@/lib/utils'
import { useStore } from '@/store'

type ApplyState =
  | { name: 'idle' }
  | { name: 'applying' }
  | { name: 'done'; result: UpdateApplyResult }
  | { name: 'failed'; detail: string }
  // The member closed the administrator dialog, or macOS refused the
  // password. Not a failure: nothing was downloaded into place.
  | { name: 'cancelled'; detail: string }
  // The check the click makes first says this machine is already on the
  // newest release. The version it was made about, so the verdict does not
  // outlive the answer behind it.
  | { name: 'current'; version: string }

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
  if (apply.name === 'current') return { name: 'idle' }
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
    note ?? result.note ?? (result.restarting ? 'Restarting the dashboard.' : '')
  return (
    <div className="space-y-1.5 text-muted-foreground">
      <p className="font-medium text-foreground">
        Updated to {result.version}.{trailing ? ` ${trailing}` : ''}
      </p>
      <p className="rounded-sm border border-border/70 bg-muted/30 px-2 py-1 font-mono text-[11px] leading-5">
        {result.updated.join(', ')}
      </p>
      {result.restart_command && (
        <div className="space-y-1">
          <p className="text-xs">The server binary beside it was replaced too. Restart the unit:</p>
          <CopyableCommand command={result.restart_command} />
        </div>
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
      <p className="text-xs leading-5 text-muted-foreground">
        Self-update is not supported on Windows. Download {cli.latest} from the
        release page and replace the binary yourself.
      </p>
    )
  }
  if (method === 'manual') {
    return (
      <div className="space-y-1.5">
        <p className="text-xs leading-5 text-muted-foreground">
          {path} is not writable by this account. Update it from a terminal:
        </p>
        <CopyableCommand command="sudo aether update" />
      </div>
    )
  }
  return (
    <Collapsible className="text-xs text-muted-foreground">
      <CollapsibleTrigger className="font-medium hover:text-foreground">
        {method === 'admin-prompt'
          ? 'Install details: macOS administrator approval'
          : 'Install details: what updating changes'}
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="mt-1.5 space-y-1.5 leading-5">
          <p>
            Updating replaces the aether binary on this machine and restarts the
            dashboard. Attached terminals and any running file sync stop with it;
            the runs themselves keep going on the server.
          </p>
          {method === 'admin-prompt' && (
            // The dialog carries osascript's name, not Aether's, and a member
            // who has never heard of osascript would rightly refuse it.
            <p>
              macOS will ask for an administrator password: {path} is in a
              directory this account cannot write to. The dialog is labelled
              osascript, the tool Aether asks through. Aether never sees your
              password.
            </p>
          )}
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}

/**
 * The CLI is behind. Every member sees this: the binary is on their own
 * machine, so no role gates it.
 */
export function CliBanner({
  update,
  client,
  recheck,
}: {
  update: UpdateStatus
  client: Api
  recheck: (refresh?: boolean) => Promise<UpdateStatus>
}) {
  const latest = update.cli.latest ?? ''
  const dismissed = useStore((s) => s.dismissedUpdates.cli)
  const setGatewayRestarting = useStore((s) => s.setGatewayRestarting)
  const setInstallingUpdate = useStore((s) => s.setInstallingUpdate)
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

  const run = async () => {
    setApply({ name: 'applying' })
    setBuild(null)
    try {
      const fresh = await recheck(true)
      if (!fresh.cli.update_available) {
        setApply({ name: 'current', version: fresh.cli.version })
        return
      }
      setInstallingUpdate(true)
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
      // password. A bearer-token refusal is 401, and neither update.check
      // nor update.apply answers 403 for anything else.
      if (err instanceof ApiError && err.status === 403) {
        setApply({ name: 'cancelled', detail: message(err) })
      } else {
        setApply({ name: 'failed', detail: message(err) })
      }
    } finally {
      setInstallingUpdate(false)
    }
  }

  const flow = deriveFlow(apply, build)
  // The click found nothing to install. The verdict is about the answer it
  // was made from, so a later release drops it and the offer comes back.
  const current =
    apply.name === 'current' &&
    apply.version === update.cli.version &&
    !update.cli.update_available
  // Once update.apply has answered, the banner reports what happened and
  // says what is left to do - the replaced paths, the server unit to
  // restart, a rebuild still running. The release check keeps moving under
  // it and answers `update_available: false` from the next read onwards, so
  // what the install said has to outlive it.
  const installed = apply.name === 'done' ? apply.result.version : ''
  if (!current && !installed && (!update.cli.update_available || !latest)) {
    return null
  }
  const version = current ? update.cli.version : installed || latest
  if (dismissed === version) return null
  // Applied names the release itself; the rebuild states have no Applied
  // block, so they carry the lead instead.
  const installLead =
    installed !== '' && flow.name !== 'applied' && flow.name !== 'rebuilt'
  const offerButton =
    !current &&
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
      ? `Downloading ${version}, then macOS asks for an administrator password...`
      : 'Updating the CLI...'
  const NoticeIcon =
    current || flow.name === 'applied' || flow.name === 'rebuilt'
      ? CheckCircle2
      : flow.name === 'applyFailed' || flow.name === 'rebuildFailed'
        ? AlertTriangle
        : flow.name === 'applying' || flow.name === 'rebuilding' || flow.name === 'relaunching'
          ? LoaderCircle
          : Download

  return (
    <div role="status" className={banner}>
      <div aria-hidden className={bannerIcon}>
        <NoticeIcon
          className={cn(
            'size-4',
            (flow.name === 'applying' ||
              flow.name === 'rebuilding' ||
              flow.name === 'relaunching') &&
              'animate-spin motion-reduce:animate-none',
          )}
        />
      </div>
      <div className={bannerContent}>
        {current && (
          <p>
            <span className="font-medium">Aether {version} is the newest release.</span>{' '}
            <span className="font-normal text-muted-foreground">Nothing was downloaded.</span>
          </p>
        )}
        {!current && !installed && (
          <>
            <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
              <p className="font-medium">Aether {version} is available.</p>
              <p className="text-xs text-muted-foreground">You are running {update.cli.version}.</p>
            </div>
            <HowItInstalls update={update} />
          </>
        )}
        {installLead && <p className="font-medium">Aether {version} is installed.</p>}
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
          <p className={cn(verbatim, 'text-state-failed')}>{flow.detail}</p>
        )}
        {flow.name === 'applyCancelled' && (
          <>
            <p className="text-muted-foreground">Update cancelled, nothing was changed.</p>
            <p className={cn(verbatim, 'text-muted-foreground')}>{flow.detail}</p>
          </>
        )}
        {flow.name === 'rebuilding' && (
          <div className="text-muted-foreground">
            <p>
              Rebuilding the app (about a minute; the first time also fetches
              Node)...
            </p>
            {flow.phase && (
              <p className="font-mono text-xs leading-5">{flow.phase}</p>
            )}
          </div>
        )}
        {flow.name === 'relaunching' && (
          <p className="text-muted-foreground">Relaunching</p>
        )}
        {flow.name === 'rebuildFailed' && (
          <div className="space-y-1.5">
            <p className={cn(verbatim, 'text-state-failed')}>{flow.error}</p>
            <p className="text-muted-foreground">Rebuild it yourself:</p>
            <CopyableCommand command="aether gui build" />
          </div>
        )}
      </div>
      {(offerButton || update.cli.release_url) && (
        <div className={bannerActions}>
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
              className={cn(
                focusRing,
                'inline-flex min-h-[22px] items-center whitespace-nowrap px-1 text-xs underline underline-offset-2 hover:text-foreground',
              )}
            >
              Release notes
            </a>
          )}
        </div>
      )}
      <Dismiss kind="cli" version={version} />
    </div>
  )
}
