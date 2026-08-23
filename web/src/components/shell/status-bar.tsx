import { Slot } from '@/components/slots'
import { ThemeToggle } from '@/components/theme'
import { formatBytes } from '@/lib/format'
import type { ConnectionState } from '@/lib/stream'
import type { DiskUsage } from '@/lib/types'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
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
  gateway: 'dashboard gateway is gone - restart aether gui (or aether dash)',
  server: 'server unreachable over SSH - check the server and network; retrying',
}

/**
 * The gauge's tooltip: what is holding the disk, in the order an operator
 * can act on it. Run checkouts are garbage-collected after their TTL,
 * transcripts live as long as their run rows, and the database is where the
 * event log accumulates. The bar says the disk is filling; this says what
 * is filling it, which is the only version an operator can act on.
 */
function diskBreakdown(disk: DiskUsage): string {
  return [
    'Disk: the filesystem holding the data directory',
    `Worktrees ${formatBytes(disk.worktree_bytes)}`,
    `Transcripts ${formatBytes(disk.transcript_bytes)}`,
    `Database ${formatBytes(disk.database_bytes)}`,
    `${formatBytes(disk.free_bytes)} free`,
  ].join(' · ')
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
        <span title={`protocol ${info.protocol_version}`}>
          aether {info.server_version}
        </span>
      )}
      {info && <span>{info.member.display_name}</span>}
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
