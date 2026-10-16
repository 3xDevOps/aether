import { useCallback, useState } from 'react'
import { useShallow } from 'zustand/react/shallow'
import { CliBanner } from '@/components/cli-update-banner'
import { CopyableCommand } from '@/components/copyable-command'
import { CircleAlert, CircleCheck, LoaderCircle, MonitorCog, ServerCog } from '@/components/icons'
import { desktopBridge } from '@/components/shell/window-bar'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Code } from '@/components/ui/code'
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
  checkedTime,
  Dismiss,
  StatusRow,
  verbatim,
} from '@/components/update-banner-shared'
import { api, type Api } from '@/lib/api'
import { useClock } from '@/lib/clock'
import { bareVersion, message } from '@/lib/format'
import type {
  ServerUpdateStatus,
  ServerUpdateWaiting,
  ServerUpdateWhen,
} from '@/lib/types'
import { memberName } from '@/lib/member-name'
import { cn, focusRing } from '@/lib/utils'
import { compareVersions } from '@/lib/version'
import { useStore } from '@/store'
import { useCapability, useIsAdmin } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import type { UpdateKind } from '@/store/ui'
import { checkForUpdates, clientTarget, serverTarget, type UpdateTarget } from '@/store/updates'

export function packagedLine(packaged: string): string {
  return `Updates come from the ${packaged}: install its new version to update Aether.`
}

/** A shell built by another release than the CLI serving it, in either
 * direction; a dev shell's "0.1.0" counts, two builds of one tag do not. */
function shellIsStale(cliVersion: string | undefined): boolean {
  const shell = desktopBridge()?.shellVersion
  if (!shell || !cliVersion) return false
  return (compareVersions(shell, cliVersion) ?? 0) !== 0
}

export function useUpdateTargets(): { server: UpdateTarget; client: UpdateTarget } {
  // The clock re-renders the timeout of a server that never came back.
  useClock()
  const server = useStore(useShallow((s) => serverTarget(s, Date.now())))
  const client = useStore(useShallow(clientTarget))
  return { server, client }
}

/** Opens the Updates dialog and runs both checks past their caches. */
export function useCheckForUpdates(client: Api = api): () => void {
  const caps = useCapability()
  const serves = caps.hasLocal('update.check')
  const reads = caps.hasMethod('server.update_status')
  return useCallback(() => {
    useStore.getState().clearDismissedUpdates()
    useStore.getState().setUpdatesOpen(true)
    void checkForUpdates(useStore, client, { client: serves, server: reads })
  }, [client, serves, reads])
}

/** The answer a check always gives, even when nothing is out of date. */
function summary(server: UpdateTarget, cli: UpdateTarget): string {
  if (server.state === 'checking' || cli.state === 'checking') return 'Checking for updates…'
  const known = [server, cli].filter((t) => t.state !== 'unknown')
  if (known.length === 0 || !known.every((t) => t.state === 'up_to_date')) return ''
  const first = known[0] as Extract<UpdateTarget, { state: 'up_to_date' }>
  const at = checkedTime(first.checkedAt)
  return `You're up to date (${first.version}).${at ? ` Checked ${at}.` : ''}`
}

export function UpdateCenter({ client = api }: { client?: Api } = {}) {
  const caps = useCapability()
  const serves = caps.hasLocal('update.check')
  const update = useStore((s) => s.update)
  const open = useStore((s) => s.updatesOpen)
  const setOpen = useStore((s) => s.setUpdatesOpen)
  const check = useCheckForUpdates(client)
  const { server, client: cli } = useUpdateTargets()
  const packaged = serves ? update?.cli.packaged : undefined
  const packagedLatest = packaged && update?.cli.update_available ? update.cli.latest : undefined
  const line = summary(server, packaged ? { state: 'unknown' } : cli)
  const checking = server.state === 'checking' || cli.state === 'checking'
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Updates</DialogTitle>
          {packaged && (
            <DialogDescription>
              {packagedLine(packaged)}
              {packagedLatest && ` Aether ${packagedLatest} is released; the app's banner says when and where to update.`}
            </DialogDescription>
          )}
          {line && <p role="status" className="text-ui text-muted">{line}</p>}
        </DialogHeader>
        <div className="min-w-0">
          <ServerRow client={client} target={server} />
          {serves && !packaged && <ClientRow client={client} target={cli} serverFirst={server.state === 'available'} />}
          {serves && <ShellBanner />}
        </div>
        <DialogFooter>
          <Button size="sm" variant="secondary" disabled={checking} onClick={check}>
            {checking ? 'Checking…' : 'Check now'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function ClientRow({ client, target, serverFirst }: { client: Api; target: UpdateTarget; serverFirst: boolean }) {
  const update = useStore((s) => s.update)
  const answered = useStore((s) => s.clientApply.name !== 'idle')
  const isAdmin = useIsAdmin()
  // A click's own answer, a cancel or "already current", stays on screen.
  if (answered && update) return <CliBanner update={update} client={client} serverFirst={serverFirst && isAdmin} />
  if (target.state === 'checking') {
    return <StatusRow Icon={LoaderCircle} spin headline="Checking for a new Aether release…" />
  }
  if (target.state === 'up_to_date') {
    const at = checkedTime(target.checkedAt)
    return (
      <StatusRow
        Icon={CircleCheck}
        headline={`Aether on this computer is up to date (${target.version}).`}
        detail={at ? `Checked ${at}.` : undefined}
      />
    )
  }
  if (target.state === 'check_failed') {
    const at = checkedTime(target.checkedAt)
    return (
      <StatusRow
        Icon={CircleAlert}
        headline="Could not check for a new Aether release."
        detail={at ? `Last successful check ${at}.` : undefined}
        error={target.error}
      />
    )
  }
  if (!update) return null
  return <CliBanner update={update} client={client} serverFirst={serverFirst && isAdmin} />
}

/** A server update already moving is named even when dismissed: it is about to restart the server. */
export function useUpdateNotice(includeDismissed = false): { text: string; action: boolean } | null {
  const caps = useCapability()
  const isAdmin = useIsAdmin()
  const update = useStore((s) => s.update)
  const stored = useStore((s) => s.dismissedUpdates)
  const dismissed = includeDismissed ? noneDismissed : stored
  const cliVersion = useStore((s) => s.capabilities?.version)
  const { server, client } = useUpdateTargets()
  const serves = caps.hasLocal('update.check')
  if (server.state === 'scheduled' || server.state === 'applying' || server.state === 'restarting') {
    return { text: 'Server update in progress, terminals reconnect briefly', action: isAdmin }
  }
  if (isAdmin && server.state === 'failed') {
    return { text: `Server update to ${bareVersion(server.latest)} failed`, action: true }
  }
  if (isAdmin && server.state === 'rolled_back') {
    return { text: 'The server update was rolled back', action: true }
  }
  if (server.state === 'available' && dismissed.server !== server.latest) {
    const text = `Server ${bareVersion(server.latest)} is available`
    return { text: isAdmin ? text : `${text}, ask an admin`, action: true }
  }
  // A packaged CLI's notice is its app's own, outside the dashboard.
  if (serves && !update?.cli.packaged && client.state === 'available' && dismissed.cli !== client.latest) {
    return { text: `Aether ${bareVersion(client.latest)} is available`, action: true }
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
          <p className="text-ui-sm text-muted">
            Built by aether {desktopBridge()?.shellVersion}; serving {cliVersion}.
          </p>
        </div>
        <p className="text-ui-sm text-muted">
          The dashboard itself is current - it ships inside the CLI. Only the
          window around it is old.
        </p>
        {buildError && (
          <div className="space-y-1">
            <p className="text-ui-sm font-medium text-state-failed">The last rebuild failed:</p>
            <p className={cn(verbatim, 'text-state-failed')}>{buildError}</p>
          </div>
        )}
        <Collapsible className="text-ui-sm text-muted">
          <CollapsibleTrigger className="font-medium hover:text-text">
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

const serverHeadline: Partial<Record<UpdateTarget['state'], (latest: string, running: string) => string>> = {
  available: (latest) => `Server ${latest} is available.`,
  scheduled: (latest) => `Server ${latest} is scheduled.`,
  applying: (latest) => `Updating the server to ${latest}…`,
  restarting: (latest) => `Restarting the server on ${latest}…`,
  failed: (latest) => `The server update to ${latest} failed.`,
  rolled_back: (_latest, running) => `The server update was rolled back; it runs ${running} again.`,
}

const applyingStep: Record<string, string> = {
  preflight: 'Checking that every client asset of the release is published. Nothing has been downloaded yet.',
  applying: 'Downloading and verifying the release. Nothing has been replaced yet.',
}

/** The local gateway advertises every method to anyone, so the admin role is checked too. */
function ServerRow({ client, target }: { client: Api; target: UpdateTarget }) {
  const status = useStore((s) => s.serverUpdate)
  const statusError = useStore((s) => s.serverUpdateError)
  const running = useStore((s) => s.serverUpdate?.server_version || s.info?.server_version || s.update?.server_version || '')
  const applyProgress = useStore((s) => s.applyServerUpdate)
  const members = useStore((s) => s.members)
  const mergedInto = useStore((s) => s.mergedInto)
  const runs = useStore((s) => s.runs)
  const pausedRuns = useStore((s) => s.pausedRuns)
  const dismissed = useStore((s) => s.dismissedUpdates.server)
  const isAdmin = useIsAdmin()
  const canUpdate = useCapability().hasMethod('server.update')
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  if (target.state === 'unknown') return null
  if (target.state === 'checking') {
    return <StatusRow Icon={LoaderCircle} spin headline="Checking for a new server release…" />
  }
  if (target.state === 'up_to_date') {
    const at = checkedTime(target.checkedAt)
    return (
      <StatusRow
        Icon={CircleCheck}
        headline={`The server is up to date (${target.version}).`}
        detail={at ? `Checked ${at}.` : undefined}
      />
    )
  }
  if (target.state === 'check_failed') {
    const at = checkedTime(target.checkedAt)
    return (
      <StatusRow
        Icon={CircleAlert}
        headline={status ? 'The server could not check for a new release.' : "Could not read the server's update status."}
        detail={at ? `Last check ${at}.` : undefined}
        error={target.error}
      />
    )
  }
  if (target.state === 'required' || target.state === 'ready_to_restart') return null
  const latest = target.latest
  if (target.state === 'available' && dismissed === latest) return null
  const headline = serverHeadline[target.state]?.(latest, running) ?? ''
  const notesUrl = target.state === 'available' ? target.notesUrl : undefined
  const failedError = target.state === 'failed' || target.state === 'rolled_back' ? target.error : ''
  const NoticeIcon = failedError || error ? CircleAlert : ServerCog

  if (!isAdmin) {
    return (
      <div role="status" className={banner}>
        <div aria-hidden className={bannerIcon}>
          <NoticeIcon className="size-4" />
        </div>
        <div className={bannerContent}>
          <p className="font-medium">{headline}</p>
          {target.state === 'available' ? (
            <p className="text-ui-sm text-muted">
              The server runs {running}. Ask an admin to run <Code>aether server update</Code>; this client
              updates after it.
            </p>
          ) : (
            <p className="text-ui-sm text-muted">
              {failedError ? 'Ask an admin to look at the server.' : 'Terminals reconnect briefly while it restarts.'}
            </p>
          )}
          {failedError && <p className={cn(verbatim, 'text-state-failed')}>{failedError}</p>}
        </div>
        {notesUrl && <div className={bannerActions}><NotesLink href={notesUrl} /></div>}
      </div>
    )
  }

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
  const scheduledBy = target.state === 'scheduled' ? memberName({ members, mergedInto }, target.by) : ''
  const offer = target.state === 'available' || target.state === 'failed' || target.state === 'rolled_back'

  return (
    <div role="status" className={banner}>
      <div aria-hidden className={bannerIcon}>
        <NoticeIcon className="size-4" />
      </div>
      <div className={bannerContent}>
        <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
          <p className="font-medium">{headline}</p>
          {target.state === 'available' && <p className="text-ui-sm text-muted">The server runs {running}.</p>}
        </div>
        {target.state === 'available' && capable && (
          <Collapsible className="text-ui-sm text-muted">
            <CollapsibleTrigger className="font-medium hover:text-text">
              What a server restart affects
            </CollapsibleTrigger>
            <CollapsibleContent>
              <p className="mt-1.5 leading-5">
                Updating replaces the server binaries and restarts the server. Runs
                keep going - the server reattaches to their containers when it comes
                back - and attached terminals reconnect on their own. Clients update
                after the server.
              </p>
            </CollapsibleContent>
          </Collapsible>
        )}
        {target.state === 'scheduled' && (
          <>
            <p className="text-muted">
              Scheduled by {scheduledBy}, applies when no run is active.
            </p>
            {waiting && <p className="text-muted">{waiting}</p>}
          </>
        )}
        {target.state === 'applying' && (
          <p className="text-muted">{applyingStep[target.step ?? 'applying'] ?? applyingStep.applying}</p>
        )}
        {target.state === 'restarting' && (
          <p className="text-muted">
            The new version is starting. Attached terminals reconnect on their own.
          </p>
        )}
        {target.state === 'failed' && (
          <p className="text-muted">The server keeps running {running}.</p>
        )}
        {failedError && <p className={cn(verbatim, 'text-state-failed')}>{failedError}</p>}
        {error && <p className={cn(verbatim, 'text-state-failed')}>{error}</p>}
        {(!capable || target.state === 'failed' || target.state === 'rolled_back') && (
          <>
            <p className="text-muted">
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
      {(notesUrl || (canUpdate && capable && (offer || target.state === 'scheduled'))) && (
        <div className={bannerActions}>
          {canUpdate && capable && target.state === 'scheduled' && (
            <Button size="sm" variant="secondary" disabled={busy} onClick={() => void act('cancel')}>
              Cancel
            </Button>
          )}
          {canUpdate && capable && offer && (
            <>
              <Button size="sm" disabled={busy} onClick={() => setConfirming(true)}>
                Update now
              </Button>
              <Button size="sm" variant="secondary" disabled={busy} onClick={() => void act('idle')}>
                Update when idle
              </Button>
            </>
          )}
          {notesUrl && <NotesLink href={notesUrl} />}
        </div>
      )}
      {target.state === 'available' && <Dismiss kind="server" version={latest} />}
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

export function NotesLink({ href }: { href: string }) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      className={cn(
        focusRing,
        'inline-flex min-h-[22px] items-center whitespace-nowrap px-1 text-ui-sm underline underline-offset-2 hover:text-text',
      )}
    >
      Release notes
    </a>
  )
}
