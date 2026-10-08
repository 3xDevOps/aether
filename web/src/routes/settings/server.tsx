import { useUpdateNotice } from '@/components/update-banner'
import { Button } from '@/components/ui/button'
import { releaseFinishedPlan } from '@/lib/commands'
import { formatBytes } from '@/lib/format'
import type { DiskUsage } from '@/lib/types'
import { workspaceRuns } from '@/routes/board/selectors'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { useStore } from '@/store'
import { useCapability, useIsAdmin, useSelf, useStateContext } from '@/store/hooks'

function diskParts(disk: DiskUsage): string[] {
  return [
    `Worktrees ${formatBytes(disk.worktree_bytes)}`,
    `Transcripts ${formatBytes(disk.transcript_bytes)}`,
    `Database ${formatBytes(disk.database_bytes)}`,
    ...(disk.repo_bytes === undefined ? [] : [`Repositories ${formatBytes(disk.repo_bytes)}`]),
    `Homes ${knownBytes(disk.home_bytes)}`,
    `Caches ${knownBytes(disk.cache_bytes)}`,
    `Evidence ${knownBytes(disk.evidence_bytes)}`,
    `Other ${knownBytes(disk.other_bytes)}`,
  ]
}

function knownBytes(bytes: number | undefined): string {
  return bytes === undefined ? 'unknown' : formatBytes(bytes)
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
      {!disk && (
        <SettingRow label="Disk" help="Storage accounting unavailable.">
          {info.diskError && <p className="whitespace-pre-wrap break-words text-ui-sm text-muted">{info.diskError}</p>}
        </SettingRow>
      )}
      {disk && (
        <SettingRow label="Disk">
          <div role="group" aria-label="Disk usage" className="flex min-w-0 flex-col gap-1.5">
            <p className="text-ui-sm text-muted">
              {disk.total_bytes > 0
                ? `${formatBytes(disk.used_bytes)} of ${formatBytes(disk.total_bytes)} used on the filesystem holding the data directory, ${formatBytes(disk.free_bytes)} free for an unprivileged writer.`
                : 'Data filesystem usage unavailable.'}
            </p>
            {disk.total_bytes > 0 && <span
              role="meter"
              aria-label="Disk used"
              aria-valuemin={0}
              aria-valuemax={disk.total_bytes}
              aria-valuenow={disk.used_bytes}
              className="block h-1.5 overflow-hidden rounded-full bg-chrome"
            >
              <span className="block h-full bg-icon-faint" style={{ width: `${Math.min(100, (disk.used_bytes / disk.total_bytes) * 100)}%` }} />
            </span>}
            <p className="text-ui-sm text-muted">Aether holds {diskParts(disk).join(' · ')}</p>
            <p className="text-ui-sm text-muted">
              Snapshots {knownBytes(disk.snapshot_bytes)} (included in worktrees). These categories attribute Aether data, not the whole filesystem or guaranteed reclaimable space.
            </p>
            <p className="text-ui-sm text-muted">
              Managed build and package caches are separate from homes, credentials and installed tools.
              Run and terminal pools belong to the launching member, even when a shared agent account is used.
              Automatic cleanup protects active, retained and uncertain owners; cache targets are not live-writer quotas.
            </p>
            {disk.warnings && disk.warnings.length > 0 && (
              <div className="text-ui-sm text-muted">
                <p>Partial measurement; affected categories may be incomplete.</p>
                <ul className="list-inside list-disc">
                  {disk.warnings.map((warning, index) => <li key={index} className="whitespace-pre-wrap break-words">{warning}</li>)}
                </ul>
              </div>
            )}
          </div>
        </SettingRow>
      )}
      {disk && (
        <SettingRow label="Docker storage (daemon-wide)">
          <div className="flex min-w-0 flex-col gap-1.5 text-ui-sm text-muted">
            {disk.docker ? (
              <>
                <p>Images {knownBytes(disk.docker.images_bytes)} · Containers {knownBytes(disk.docker.containers_bytes)} · Volumes {knownBytes(disk.docker.volumes_bytes)} · Build cache {knownBytes(disk.docker.build_cache_bytes)}</p>
                <p>These figures cover the entire Docker daemon, including workloads outside Aether. Bind-backed homes are counted under Aether homes, not again as Docker volumes.</p>
                <p>Docker reports {knownBytes(disk.docker.reclaimable_bytes)} as reclaimable using its own unused classification. This is not Aether authorization to delete saved images; an unused image may still be needed by Aether.</p>
                <p>Docker filesystem: {knownBytes(disk.docker.used_bytes)} used · {knownBytes(disk.docker.total_bytes)} total · {knownBytes(disk.docker.free_bytes)} free.</p>
                <p>
                  {disk.docker.shared_filesystem === undefined
                    ? 'Whether Docker shares the data filesystem is unknown.'
                    : disk.docker.shared_filesystem
                      ? 'Docker shares the data filesystem.'
                      : 'Docker uses a separate filesystem.'}
                  {' '}Filesystem figures are not added together.
                </p>
                {disk.docker.error && <p className="whitespace-pre-wrap break-words">{disk.docker.error}</p>}
              </>
            ) : <p>Docker accounting unavailable.</p>}
          </div>
        </SettingRow>
      )}
      {disk?.entries && <StorageOwners disk={disk} />}
      {admin && <RetainedContainersRow />}
    </SettingsSection>
  )
}

function StorageOwners({ disk }: { disk: DiskUsage }) {
  const runs = useStore((s) => s.runs)
  const members = useStore((s) => s.members)
  const workspaces = useStore((s) => s.workspaces)
  return (
    <SettingRow label="Largest storage owners" help="Attribution overlaps the categories above; these bytes are not additional totals.">
      <details className="min-w-0 text-ui-sm text-muted">
        <summary className="cursor-pointer py-2">
          {disk.entries?.length ?? 0} owners shown{disk.truncated ? ' · Truncated to the largest 50 entries' : ''}
        </summary>
        <ul className="flex min-w-0 flex-col gap-3">
          {disk.entries?.map((entry, index) => {
            const id = entry.owner_id
            const name = id && (entry.owner_kind === 'workspace'
              ? workspaces[id]?.name
              : entry.owner_kind === 'member'
                ? members[id]?.display_name
                : entry.owner_kind === 'run'
                  ? runs[id]?.title
                  : undefined)
            return (
              <li key={index} className="min-w-0 break-words">
                <p className="font-medium text-text">{entry.owner_kind}{name ? ` ${name}` : ''}{id ? ` (${id})` : ''} · {entry.kind}{entry.pool ? ` (${entry.pool})` : ''} · {formatBytes(entry.bytes)}</p>
                <p>{entry.reason}</p>
                <p>
                  Reclaimable: {knownBytes(entry.reclaimable_bytes)}
                  {entry.retained_until && <> · {entry.kind === 'cache' ? 'Age target' : 'Retained until'} <time dateTime={entry.retained_until}>{new Date(entry.retained_until).toLocaleString()}</time></>}
                </p>
                {entry.error && <p className="whitespace-pre-wrap">{entry.error}</p>}
              </li>
            )
          })}
        </ul>
      </details>
    </SettingRow>
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
        ? `${count} finished ${count === 1 ? 'run keeps its' : 'runs keep their'} container${where}. Removing containers prevents reopening those runs; it does not delete all run data, homes, repositories or history.`
        : `No finished run keeps a container${where}.`}
      control={(
        <Button size="sm" variant="secondary" disabled={count === 0} onClick={() => openDialog('release-finished')}>
          Free retained containers…
        </Button>
      )}
    />
  )
}
