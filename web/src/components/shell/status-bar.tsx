import { Slot } from '@/components/slots'
import { ThemeToggle } from '@/components/theme'
import { formatBytes } from '@/lib/format'
import type { ConnectionState } from '@/lib/stream'
import type { DiskUsage } from '@/lib/types'
import { cn } from '@/lib/utils'
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
// restarted, and a dead SSH hop needs the server or the tunnel looked at
// while the gateway keeps retrying on its own.
const unreachableLabel: Record<UnreachableKind, string> = {
  network: 'this computer is offline - reconnect to wifi or your VPN',
  gateway: 'dashboard gateway is gone - restart aether gui',
  server: 'server unreachable over SSH - check the server and network; retrying',
}

/**
 * The gauge's tooltip: what is holding the disk, in the order an operator
 * can act on it. Run checkouts are garbage-collected after their TTL,
 * transcripts live as long as their run rows, the database is where the
 * event log accumulates, and the bare workspace repos keep every push and
 * run branch. The bar says the disk is filling; this says what is filling
 * it, which is the only version an operator can act on.
 *
 * The repos line is dropped rather than shown as zero when the server
 * predates the component, so an old server reads as silent instead of as a
 * server with no repositories.
 */
function diskBreakdown(disk: DiskUsage): string {
  return [
    'Disk: the filesystem holding the data directory',
    `Worktrees ${formatBytes(disk.worktree_bytes)}`,
    `Transcripts ${formatBytes(disk.transcript_bytes)}`,
    `Database ${formatBytes(disk.database_bytes)}`,
    ...(disk.repo_bytes === undefined
      ? []
      : [`Repos ${formatBytes(disk.repo_bytes)}`]),
    `${formatBytes(disk.free_bytes)} free`,
  ].join(' · ')
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
    <span
      role="status"
      className="rounded-full bg-state-waiting/15 px-2 py-0.5 text-[11px] font-medium"
    >
      {notice}
    </span>
  )
}

/**
 * The desktop gateway's always-visible entry point: whether this machine
 * has a linked repository, jumping to onboarding until it does and to
 * settings after. Gated on the link.status verb, so the remote gateway
 * shows nothing.
 */
function LocalStatus() {
  const cap = useCapability()
  const link = useStore((s) => s.linkStatus)
  const navigate = useStore((s) => s.navigate)
  if (!cap.hasLocal('link.status')) return null
  const linked = link?.linked === true
  return (
    <button
      type="button"
      onClick={() => navigate(linked ? 'settings' : 'onboarding')}
      title={linked ? `Linked to ${link?.repo}` : 'Link a repository'}
      className="flex items-center gap-1.5 rounded px-1 hover:text-foreground"
    >
      <span
        className={cn(
          'size-2 rounded-full',
          linked ? 'bg-state-done' : 'bg-state-waiting',
        )}
        aria-hidden
      />
      {linked ? 'Linked' : 'Not linked'}
    </button>
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

  if (!available) return <span title={`protocol ${protocol}`}>{label}</span>

  const latest = update.cli.latest ?? ''
  return (
    <button
      type="button"
      onClick={clearDismissedUpdates}
      aria-label={`Update available: ${latest}`}
      title={`${latest} is available - show the update banner`}
      className="flex items-center gap-1.5 rounded px-1 hover:text-foreground"
    >
      {label}
      <span className="size-2 rounded-full bg-state-waiting" aria-hidden />
    </button>
  )
}

export function StatusBar() {
  const connection = useStore((s) => s.connection)
  const unreachable = useStore((s) => s.unreachable)
  const info = useStore((s) => s.info)
  const disk = info?.disk

  return (
    <footer className="flex h-8 shrink-0 items-center gap-3 border-t px-3 text-xs text-muted-foreground">
      <span className="flex items-center gap-1.5">
        <span
          className={cn('size-2 rounded-full', connectionDot[connection])}
          aria-hidden
        />
        {connectionLabel[connection]}
      </span>
      {unreachable !== null && (
        <span
          role="status"
          className="rounded-full bg-state-needs-attention/15 px-2 py-0.5 text-[11px] font-medium text-state-needs-attention"
        >
          {unreachableLabel[unreachable]}
        </span>
      )}
      {info && (
        <VersionLabel
          version={info.server_version}
          protocol={info.protocol_version}
        />
      )}
      {info && <span>{info.member.display_name}</span>}
      <ServerUpdateNotice />
      {disk && disk.total_bytes > 0 && (
        <span
          className="flex items-center gap-1.5"
          aria-label="Disk usage"
          title={diskBreakdown(disk)}
        >
          <span className="h-1.5 w-16 overflow-hidden rounded-full bg-muted">
            <span
              className="block h-full bg-foreground/50"
              style={{
                width: `${Math.min(100, (disk.used_bytes / disk.total_bytes) * 100)}%`,
              }}
            />
          </span>
          {formatBytes(disk.used_bytes)} / {formatBytes(disk.total_bytes)}
        </span>
      )}
      <LocalStatus />
      <span className="ml-auto flex items-center gap-3">
        <Slot name="statusbar" />
        <ThemeToggle />
      </span>
    </footer>
  )
}
