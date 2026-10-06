import { useUpdateNotice } from '@/components/update-banner'
import { Button } from '@/components/ui/button'
import { releaseFinishedPlan } from '@/lib/commands'
import { formatBytes } from '@/lib/format'
import type { DiskUsage } from '@/lib/types'
import { workspaceRuns } from '@/routes/board/selectors'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { useStore } from '@/store'
import { useCapability, useIsAdmin, useSelf, useStateContext } from '@/store/hooks'

/** An old server reports no repos figure; it is left out rather than shown as zero. */
function diskParts(disk: DiskUsage): string[] {
  return [
    `Worktrees ${formatBytes(disk.worktree_bytes)}`,
    `Transcripts ${formatBytes(disk.transcript_bytes)}`,
    `Database ${formatBytes(disk.database_bytes)}`,
    ...(disk.repo_bytes === undefined ? [] : [`Repositories ${formatBytes(disk.repo_bytes)}`]),
  ]
}

export function ServerSection() {
  const info = useStore((s) => s.info)
  const admin = useIsAdmin()
  const update = useUpdateNotice(true)
  const openUpdates = useStore((s) => s.setUpdatesOpen)
  const clearDismissed = useStore((s) => s.clearDismissedUpdates)
  const disk = info?.disk
  if (!info) return null
  return (
    <SettingsSection title="Server">
      <SettingRow
        label="Version"
        help={update ? `Aether ${info.server_version}, protocol ${info.protocol_version}. ${update.text}.` : `Aether ${info.server_version}, protocol ${info.protocol_version}.`}
        control={update?.action && (
          <Button
            size="sm"
            onClick={() => {
              clearDismissed()
              openUpdates(true)
            }}
          >
            Update…
          </Button>
        )}
      />
      {disk && disk.total_bytes > 0 && (
        <SettingRow label="Disk">
          <div role="group" aria-label="Disk usage" className="flex min-w-0 flex-col gap-1.5">
            <p className="text-ui-sm text-muted">
              {formatBytes(disk.used_bytes)} of {formatBytes(disk.total_bytes)} used on the filesystem holding the data directory, {formatBytes(disk.free_bytes)} free.
            </p>
            <span
              role="meter"
              aria-label="Disk used"
              aria-valuemin={0}
              aria-valuemax={disk.total_bytes}
              aria-valuenow={disk.used_bytes}
              className="block h-1.5 overflow-hidden rounded-full bg-chrome"
            >
              <span className="block h-full bg-icon-faint" style={{ width: `${Math.min(100, (disk.used_bytes / disk.total_bytes) * 100)}%` }} />
            </span>
            <p className="text-ui-sm text-muted">Aether holds {diskParts(disk).join(' · ')}</p>
          </div>
        </SettingRow>
      )}
      {admin && <RetainedContainersRow />}
    </SettingsSection>
  )
}

function RetainedContainersRow() {
  const ctx = useStateContext()
  const caps = useCapability()
  const self = useSelf()
  const activeWorkspace = useStore((s) => s.activeWorkspace)
  const workspaceName = useStore((s) => s.workspaces[s.activeWorkspace]?.name)
  const openDialog = useStore((s) => s.openPaletteDialog)
  if (!caps.hasMethod('run.release')) return null
  const count = releaseFinishedPlan(workspaceRuns(activeWorkspace, ctx), caps, self).eligible.length
  const where = workspaceName ? ` in ${workspaceName}` : ''
  return (
    <SettingRow
      label="Retained containers"
      help={count > 0
        ? `${count} finished ${count === 1 ? 'run keeps its' : 'runs keep their'} container${where}. Freeing them reclaims disk; those runs cannot be reopened.`
        : `No finished run keeps a container${where}.`}
      control={(
        <Button size="sm" variant="secondary" disabled={count === 0} onClick={() => openDialog('release-finished')}>
          Free retained containers…
        </Button>
      )}
    />
  )
}
