import { formatBytes } from '@/lib/format'
import type { DiskUsage } from '@/lib/types'
import { UsageReader } from '@/routes/settings/usage'
import { useStore } from '@/store'

/**
 * What is holding the disk, in the order an operator can act on it. The
 * repos line is dropped rather than shown as zero when the server predates
 * the component, so an old server reads as silent.
 */
function diskLines(disk: DiskUsage): string[] {
  return [
    `Worktrees ${formatBytes(disk.worktree_bytes)}`,
    `Transcripts ${formatBytes(disk.transcript_bytes)}`,
    `Database ${formatBytes(disk.database_bytes)}`,
    ...(disk.repo_bytes === undefined ? [] : [`Repos ${formatBytes(disk.repo_bytes)}`]),
    `${formatBytes(disk.free_bytes)} free`,
  ]
}

export function ServerSection() {
  const info = useStore((s) => s.info)
  const disk = info?.disk
  return (
    <section aria-labelledby="server-heading" className="min-w-0 space-y-3 border-b border-seam py-4">
      <h2 id="server-heading" className="text-title">Server</h2>
      {info && (
        <p className="text-ui text-muted">
          Aether {info.server_version}, protocol {info.protocol_version}
        </p>
      )}
      {disk && disk.total_bytes > 0 && (
        <div aria-label="Disk usage" title={diskLines(disk).join(' · ')} className="max-w-md space-y-1">
          <p className="text-ui">
            Disk: {formatBytes(disk.used_bytes)} of {formatBytes(disk.total_bytes)} used on the filesystem holding the data directory
          </p>
          <span className="block h-1.5 overflow-hidden rounded-full bg-chrome">
            <span
              className="block h-full bg-icon-faint"
              style={{ width: `${Math.min(100, (disk.used_bytes / disk.total_bytes) * 100)}%` }}
            />
          </span>
          <p className="text-ui-sm text-muted">{diskLines(disk).join(' · ')}</p>
        </div>
      )}
      <UsageReader />
    </section>
  )
}
