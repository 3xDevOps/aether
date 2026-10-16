import { useEffect, useRef, useState, type ReactNode } from 'react'
import { useShallow } from 'zustand/react/shallow'
import { CopyableCommand } from '@/components/copyable-command'
import { ArrowUpCircle, CircleAlert, LoaderCircle } from '@/components/icons'
import { desktopBridge } from '@/components/shell/window-bar'
import { Button } from '@/components/ui/button'
import { ManualInstall, manualInstallLine, verbatim } from '@/components/update-banner-shared'
import { api, type Api } from '@/lib/api'
import { copyText } from '@/lib/clipboard'
import { message } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import { applyClientUpdate, clientTarget, installTarget, manualCommand } from '@/store/updates'

const buildStep: Record<string, string> = {
  unpacking: 'Unpacking the desktop app…',
  'fetching node': 'Fetching Node for the desktop app (first time only)…',
  'installing dependencies': 'Installing the desktop app dependencies…',
  packaging: 'Packaging the desktop app…',
  installing: 'Installing the desktop app…',
}

/**
 * The server refuses this client until it updates. Rendered in place of the
 * whole dashboard, never dismissible; the way out is updating, another
 * server, or a reload.
 */
export function UpdateGate({ client = api }: { client?: Api }) {
  const caps = useCapability()
  const required = useStore((s) => s.updateRequired)
  const update = useStore((s) => s.update)
  const build = useStore((s) => s.clientBuild)
  const link = useStore((s) => s.linkStatus)
  const target = useStore(useShallow(clientTarget))
  const cancelled = useStore((s) => (s.clientApply.name === 'cancelled' ? s.clientApply.detail : ''))
  const [switchError, setSwitchError] = useState<string | null>(null)
  const detailsRef = useRef<HTMLPreElement>(null)
  const headingRef = useRef<HTMLHeadingElement>(null)
  useEffect(() => headingRef.current?.focus(), [])

  const clientVersion = required?.client_version || update?.cli.version || ''
  const serverVersion = required?.server_version || update?.server_version || ''
  const minimum = required?.min_client_version || update?.min_client_version || serverVersion
  const reason =
    required?.message ||
    `this aether is ${clientVersion} and the server runs ${serverVersion}; update this client first, then retry`
  const command = manualCommand(update)
  const cannotSelfUpdate = update !== null && update.can_self_update !== true
  const release = installTarget(update, true)
  const canApply = !cannotSelfUpdate && caps.hasLocal('update.apply') && update?.install_method !== 'manual'
  // The running gateway keeps the binary it started with until it restarts.
  const desktop = desktopBridge() !== undefined
  const afterTerminal: ReactNode = desktop ? (
    'Then quit and reopen the desktop app.'
  ) : (
    <>
      Then restart <code className="font-code">aether gui</code> and reload this page.
    </>
  )
  const busy = target.state === 'applying' || target.state === 'restarting'
  const error = target.state === 'failed' ? target.error : ''
  const others = [
    ...(link?.default && (link.active ?? '') !== '' ? [{ name: '', label: 'Default', addr: link.default.addr }] : []),
    ...(link?.links ?? [])
      .filter((l) => l.name !== (link?.active ?? ''))
      .map((l) => ({ name: l.name, label: l.name, addr: l.addr })),
  ]
  const details = [
    `Aether update required`,
    `Server: ${reason}`,
    `This client: ${clientVersion}`,
    `Server version: ${serverVersion}`,
    `Minimum client: ${minimum}`,
    update && cannotSelfUpdate ? manualInstallLine(update, release) : `Manual command: ${command}`,
    ...(error ? [`Update error: ${error}`] : []),
    ...(cancelled ? [`Update cancelled: ${cancelled}`] : []),
  ].join('\n')

  const switchTo = async (name: string) => {
    setSwitchError(null)
    try {
      await client.localLinkSwitch(name)
      window.location.reload()
    } catch (err) {
      setSwitchError(message(err))
    }
  }

  return (
    <main className="flex h-full min-h-0 min-w-0 overflow-y-auto bg-canvas p-3 sm:p-4">
      <section
        aria-labelledby="update-gate-title"
        className="m-auto grid min-w-0 w-full max-w-[720px] overflow-hidden border border-seam bg-canvas"
      >
        <header className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] items-start gap-3 border-b border-seam bg-chrome px-3 py-3 sm:px-4">
          <div className="grid size-7 shrink-0 place-items-center rounded-control bg-state-needs-you/15 text-state-needs-you">
            <ArrowUpCircle className="size-4" aria-hidden />
          </div>
          <div className="min-w-0">
            <p className="mb-0.5 text-ui-sm font-medium text-muted">Update required</p>
            <h1 id="update-gate-title" ref={headingRef} tabIndex={-1} className="text-title leading-5 outline-none">
              Update Aether to keep using this server
            </h1>
          </div>
        </header>

        <div className="min-w-0 space-y-3 px-3 py-3 sm:px-4">
          <p className="max-w-[68ch] text-ui leading-5 text-muted">
            The server said: <span className="text-text">{reason}</span>. It accepts clients from {minimum}.
            Updating changes only this computer.
          </p>

          {target.state === 'ready_to_restart' ? (
            <div className="space-y-2">
              <p className="font-medium">Aether {target.latest} is installed.</p>
              {desktop ? (
                <p className="text-ui-sm text-muted">Quit and reopen the desktop app to use it.</p>
              ) : (
                <>
                  <p className="text-ui-sm text-muted">
                    This dashboard is still served by the old <code className="font-code">aether gui</code>. Stop it,
                    start it again, then reload.
                  </p>
                  <Button size="sm" onClick={() => window.location.reload()}>Reload</Button>
                </>
              )}
            </div>
          ) : update && cannotSelfUpdate ? (
            <ManualInstall update={update} target={release} />
          ) : (
            <div className="space-y-2">
              {canApply && (
                <Button size="sm" disabled={busy} onClick={() => void applyClientUpdate(useStore, client)}>
                  {busy && <LoaderCircle className="animate-spin motion-reduce:animate-none" aria-hidden />}
                  {target.state === 'applying' ? 'Updating…' : target.state === 'restarting' ? 'Relaunching…' : 'Update now'}
                </Button>
              )}
              <div role="status" className="space-y-1 text-ui-sm">
                {target.state === 'applying' && (
                  <p className="text-muted">
                    {build?.phase && buildStep[build.phase]
                      ? buildStep[build.phase]
                      : `Downloading and verifying ${target.latest || 'the release'}…`}
                  </p>
                )}
                {target.state === 'restarting' && (
                  <p className="text-muted">Restarting the dashboard on the new version…</p>
                )}
                {error && (
                  <>
                    <p className="flex items-center gap-1.5 font-medium text-state-failed">
                      <CircleAlert className="size-3.5" aria-hidden />
                      The update failed. Nothing on the server changed.
                    </p>
                    <p className={cn(verbatim, 'text-state-failed')}>{error}</p>
                  </>
                )}
                {cancelled && (
                  <>
                    <p className="text-muted">Update cancelled, nothing was changed.</p>
                    <p className="whitespace-pre-wrap break-words font-code text-muted">{cancelled}</p>
                  </>
                )}
              </div>
              <p className="text-ui-sm text-muted">
                {canApply ? 'Or update from a terminal on this computer:' : 'Update from a terminal on this computer:'}
              </p>
              <CopyableCommand command={command} />
              <p className="text-ui-sm text-muted">{afterTerminal}</p>
            </div>
          )}

          <div className="flex flex-wrap items-center gap-1 border-t border-seam pt-3">
            <Button size="sm" variant="secondary" onClick={() => void copyText(details, detailsRef.current)}>
              Copy details
            </Button>
            <Button size="sm" variant="secondary" onClick={() => window.location.reload()}>
              Reload
            </Button>
            {others.map((other) => (
              <Button key={other.name} size="sm" variant="secondary" onClick={() => void switchTo(other.name)}>
                Switch to {other.label} ({other.addr})
              </Button>
            ))}
          </div>
          {switchError && <p className={cn(verbatim, 'text-state-failed')}>{switchError}</p>}
          <pre ref={detailsRef} aria-hidden className="sr-only">{details}</pre>
        </div>
      </section>
    </main>
  )
}
