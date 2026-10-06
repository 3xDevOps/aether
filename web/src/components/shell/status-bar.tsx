import { useEffect, useRef, useState } from 'react'
import { Slot } from '@/components/slots'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Badge } from '@/components/ui/badge'
import { Tooltip } from '@/components/ui/tooltip'
import { formatBytes } from '@/lib/format'
import { UsageReader } from '@/components/shell/usage'
import { inModal } from '@/lib/keys'
import type { ConnectionState } from '@/lib/stream'
import type { DiskUsage } from '@/lib/types'
import { cn, focusRing } from '@/lib/utils'
import { TeamStatusDetails } from '@/routes/team'
import { useStore } from '@/store'
import { useCapability, useIsAdmin } from '@/store/hooks'
import type { UnreachableKind } from '@/store/server'

const connectionLabel: Record<ConnectionState, string> = {
  connecting: 'Connecting',
  live: 'Live',
  reconnecting: 'Reconnecting',
  offline: 'Offline',
}

// Health reuses the run state tokens: nothing new to keep in sync.
const connectionDot: Record<ConnectionState, string> = {
  connecting: 'bg-state-waiting',
  live: 'bg-state-done',
  reconnecting: 'bg-state-waiting',
  offline: 'bg-state-failed',
}

// Which hop is down decides what an operator does next: a dead local
// network needs wifi or a VPN back, a dead gateway origin needs its process
// restarted, a phone off the tailnet needs Tailscale or the server host, a
// link through an edge needs whichever of the edge, the server's edge
// connection, the sign-in or the device failed, and a dead SSH hop needs the
// server or the tunnel looked at while the gateway keeps retrying on its own.
const unreachableLabel: Record<UnreachableKind, string> = {
  network: 'this computer is offline - reconnect to wifi or your VPN',
  gateway: 'dashboard gateway is gone - restart aether gui',
  tailnet: 'no answer from your server - check Tailscale and the server host',
  server: 'server unreachable over SSH - check the server and network; retrying',
  edge: 'edge unreachable - check this computer can reach it; retrying',
  'edge-server': 'server not connected to the edge - check aether-server edge status',
  'edge-refused': 'the edge refused the connection to this server',
  'signed-out': 'signed out of the edge - run aether login',
  'not-member': 'not a member of this server - ask an admin to invite you again',
  'device-revoked': 'this device was revoked on the server',
  'device-pending': 'this device is waiting for approval - approve its code from another device or on the server',
  refused: 'the gateway refused this device - it is not identified as a member',
  identity: 'the server cannot identify this device - check tailscaled on the server host',
}

/**
 * What is holding the disk, in the order an operator can act on it. The
 * gauge's tooltip joins these for a pointer; the compact status popup lists
 * them, because touch has no tooltip. Run checkouts are garbage-collected after their TTL,
 * transcripts live as long as their run rows, the database is where the
 * event log accumulates, and the bare workspace repos keep every push and
 * run branch. The bar says the disk is filling; this says what is filling
 * it, which is the only version an operator can act on.
 *
 * The repos line is dropped rather than shown as zero when the server
 * predates the component, so an old server reads as silent instead of as a
 * server with no repositories.
 */
function diskLines(disk: DiskUsage): string[] {
  return [
    'Disk: the filesystem holding the data directory',
    `Worktrees ${formatBytes(disk.worktree_bytes)}`,
    `Transcripts ${formatBytes(disk.transcript_bytes)}`,
    `Database ${formatBytes(disk.database_bytes)}`,
    ...(disk.repo_bytes === undefined
      ? []
      : [`Repos ${formatBytes(disk.repo_bytes)}`]),
    `${formatBytes(disk.free_bytes)} free`,
  ]
}

/**
 * What a member who cannot press the update buttons is told while the
 * server updates itself. Both phases end in the same restart, and a
 * restart nobody explained looks like an outage; an admin has the banner
 * instead, which says the same thing with the controls attached.
 */
function ServerUpdateNotice() {
  const isAdmin = useIsAdmin()
  const progress = useStore((s) => s.serverUpdateProgress)
  const pending = useStore((s) => s.serverUpdate?.pending)
  const phase = progress?.phase ?? (pending ? 'scheduled' : undefined)
  if (isAdmin) return null
  const notice =
    phase === 'scheduled'
      ? 'server update scheduled, terminals will reconnect briefly'
      : phase === 'applying' || phase === 'restarting'
        ? 'server update applying, terminals will reconnect briefly'
        : null
  if (!notice) return null
  return (
    // Keep the complete notice available to both pointer and touch readers.
    <span
      role="status"
      title={notice}
      className="flex min-h-[var(--status-bar-height)] min-w-0 items-center break-words whitespace-normal"
    >
      <Badge className="h-auto min-h-5 min-w-0 shrink">
        <span className="min-w-0 break-words whitespace-normal">
          {notice}
        </span>
      </Badge>
    </span>
  )
}

/** Local repository state, never another path into navigation. */
function LocalStatus() {
  const cap = useCapability()
  const link = useStore((s) => s.linkStatus)
  if (!cap.hasLocal('link.status')) return null
  const linked = link?.linked === true
  return (
    <span className="flex min-h-[var(--status-bar-height)] min-w-0 items-center gap-1">
      <span
        className={cn(
          'size-2 shrink-0 rounded-full',
          link === null ? 'bg-muted-foreground' : linked ? 'bg-state-done' : 'bg-state-waiting',
        )}
        aria-hidden
      />
      {link === null ? 'Link status unknown' : linked ? 'Linked' : 'Not linked'}
    </span>
  )
}

/**
 * The version label, with a dot when a newer release is out. The banner is
 * the thing that says what to do about it, so the badge's only job is to
 * bring a dismissed one back - clicking it clears the dismissals rather than
 * navigating anywhere.
 */
function VersionLabel({ version, protocol }: { version: string; protocol: string }) {
  const update = useStore((s) => s.update)
  const isAdmin = useIsAdmin()
  const clearDismissedUpdates = useStore((s) => s.clearDismissedUpdates)
  const label = `aether ${version}`
  const available =
    update !== null &&
    (update.cli.update_available || (update.server_behind && isAdmin))

  if (!available) {
    return (
      <span
        className="flex min-h-[var(--status-bar-height)] min-w-0 items-center break-words whitespace-normal"
        title={`${label} · protocol ${protocol}`}
      >
        {label}
      </span>
    )
  }

  const latest = update.cli.latest ?? ''
  return (
    <Tooltip content={<>{latest} is available - show the update banner</>}>
      <button
        type="button"
        onClick={() => {
          clearDismissedUpdates()
        }}
        aria-label={`Update available: ${latest}`}
        className={cn(
          focusRing,
          'flex min-h-[var(--status-bar-height)] min-w-0 items-center gap-1 rounded-sm px-1 break-words whitespace-normal hover:text-foreground',
        )}
      >
        {label}
        <span className="size-2 rounded-full bg-state-waiting" aria-hidden />
      </button>
    </Tooltip>
  )
}

/**
 * The facts a pointer reads from a tooltip or a `title`, written out. Touch
 * has neither, and the compact popup is the one place in the shell with room
 * for a sentence, so the disk breakdown, the protocol version and what this
 * machine is linked to are rows here rather than hints only a mouse can find.
 */
function StatusFacts({ disk }: { disk?: DiskUsage }) {
  const info = useStore((s) => s.info)
  const cap = useCapability()
  const link = useStore((s) => s.linkStatus)
  const rows = [
    ...(info ? [`Protocol ${info.protocol_version}`] : []),
    ...(cap.hasLocal('link.status') && link?.linked === true
      ? [`Linked to ${link.repo}`]
      : []),
    ...(disk && disk.total_bytes > 0 ? diskLines(disk) : []),
  ]
  if (rows.length === 0) return null
  return (
    <ul className="flex min-w-0 flex-col gap-0.5 border-t border-border pt-1 text-[12px] leading-4 text-muted-foreground">
      {rows.map((row) => (
        <li key={row} className="min-w-0 break-words">
          {row}
        </li>
      ))}
    </ul>
  )
}

// Keep connection, shortcuts and phone attention outside the disclosure.
// Both the outer Slot and the secondary readouts have one permanent home.
export function StatusBar() {
  const connection = useStore((s) => s.connection)
  const unreachable = useStore((s) => s.unreachable)
  const info = useStore((s) => s.info)
  const disk = info?.disk
  const [detailsOpen, setDetailsOpen] = useState(false)
  const details = useRef<HTMLDivElement>(null)

  // Capture Escape before the shell can leave the current run. Portalled
  // Usage/select/dialog overlays own their interactions first. forceMount
  // preserves readout state while closed; the outer Slot never moves.
  useEffect(() => {
    if (!detailsOpen) return
    const outside = (event: PointerEvent) => {
      if (details.current?.contains(event.target as Node | null) || inModal(event.target)) return
      setDetailsOpen(false)
    }
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== 'Escape' || event.defaultPrevented || inModal(event.target)) return
      event.preventDefault()
      setDetailsOpen(false)
      // A dismissed popup can be holding the focus; the trigger is where it
      // came from and where it opens again.
      details.current
        ?.querySelector<HTMLElement>('[aria-controls="status-details"]')
        ?.focus()
    }
    window.addEventListener('pointerdown', outside, true)
    window.addEventListener('keydown', onKey, true)
    return () => {
      window.removeEventListener('pointerdown', outside, true)
      window.removeEventListener('keydown', onKey, true)
    }
  }, [detailsOpen])

  return (
    <footer className="relative flex min-h-[var(--status-bar-height)] shrink-0 items-center gap-1 border-0 bg-sidebar py-0 pr-[max(0.5rem,env(safe-area-inset-right))] pb-[env(safe-area-inset-bottom)] pl-[max(0.5rem,env(safe-area-inset-left))] text-[12px] leading-none text-muted-foreground before:pointer-events-none before:absolute before:inset-x-0 before:top-0 before:h-px before:bg-border before:content-['']">
      <div className="flex min-w-0 flex-1 items-center gap-2">
        <span className="flex h-[var(--status-bar-height)] shrink-0 items-center gap-1">
          <span
            className={cn('size-1.5 rounded-full', connectionDot[connection])}
            aria-hidden
          />
          {connectionLabel[connection]}
        </span>
        <Collapsible
          ref={details}
          className="relative block h-[var(--status-bar-height)] min-w-0 leading-none"
          open={detailsOpen}
          onOpenChange={setDetailsOpen}
        >
          <Tooltip content="Show status details">
            <CollapsibleTrigger
              className="h-[var(--status-bar-height)] min-h-[var(--status-bar-height)] w-[var(--status-bar-height)] justify-center rounded-sm border border-transparent text-muted-foreground hover:border-border hover:bg-toolbar-hover hover:text-foreground"
              aria-label="Show status details"
              aria-controls="status-details"
            />
          </Tooltip>
          <CollapsibleContent
            id="status-details"
            forceMount
            className="block min-w-0 data-[state=closed]:hidden"
          >
            <div
              className="fixed inset-x-2 bottom-[calc(var(--status-bar-height)_+_0.375rem_+_env(safe-area-inset-bottom))] z-50 mb-1 flex max-h-[min(70dvh,calc(100dvh_-_var(--status-bar-height)_-_env(safe-area-inset-bottom)_-_1rem))] min-w-0 max-w-md flex-col items-stretch gap-1 overflow-y-auto rounded-sm border border-border bg-popover p-2 text-popover-foreground leading-normal shadow-lg"
            >
              {unreachable !== null && (
                <span
                  role="status"
                  title={unreachableLabel[unreachable]}
                  className="flex min-h-[var(--status-bar-height)] min-w-0 items-center break-words whitespace-normal"
                >
                  <Badge tone="needs-you" className="h-auto min-h-5 min-w-0 shrink">
                    <span className="min-w-0 break-words whitespace-normal">
                      {unreachableLabel[unreachable]}
                    </span>
                  </Badge>
                </span>
              )}
              <ServerUpdateNotice />
              {info && (
                <VersionLabel
                  version={info.server_version}
                  protocol={info.protocol_version}
                />
              )}
              <LocalStatus />
              {info && (
                <span
                  title={info.member.display_name}
                  className="flex min-h-[var(--status-bar-height)] min-w-0 items-center break-words whitespace-normal"
                >
                  {info.member.display_name}
                </span>
              )}
              {disk && disk.total_bytes > 0 && (
                <span
                  className="flex min-h-[var(--status-bar-height)] min-w-0 items-center gap-1 break-words whitespace-normal"
                  aria-label="Disk usage"
                  title={diskLines(disk).join(' · ')}
                >
                  <span className="h-1.5 w-16 shrink-0 overflow-hidden rounded-sm bg-muted">
                    <span
                      className="block h-full bg-foreground/50"
                      style={{
                        width: `${Math.min(100, (disk.used_bytes / disk.total_bytes) * 100)}%`,
                      }}
                    />
                  </span>
                  <span className="min-w-0 break-words">
                    {formatBytes(disk.used_bytes)} / {formatBytes(disk.total_bytes)}
                  </span>
                </span>
              )}
              <StatusFacts disk={disk} />
              <TeamStatusDetails />
              <UsageReader />
            </div>
          </CollapsibleContent>
        </Collapsible>
      </div>
      <span className="flex min-w-0 shrink-0 items-center gap-2">
        <Slot name="statusbar" />
      </span>
    </footer>
  )
}
