import { Slot } from '@/components/slots'
import { ThemeToggle } from '@/components/theme'
import { formatBytes } from '@/lib/format'
import type { ConnectionState } from '@/lib/stream'
import type { DiskUsage } from '@/lib/types'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'

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

export function StatusBar() {
  const connection = useStore((s) => s.connection)
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
      <span className="ml-auto flex items-center gap-3">
        <Slot name="statusbar" />
        <ThemeToggle />
      </span>
    </footer>
  )
}
