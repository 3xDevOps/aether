import { useCheckForUpdates, useUpdateNotice, useUpdateTargets } from '@/components/update-banner'
import { checkedTime, packagedLine, verbatim } from '@/components/update-banner-shared'
import { CopyableCommand } from '@/components/copyable-command'
import { Button } from '@/components/ui/button'
import { CodeBlock } from '@/components/ui/code'
import { Spinner } from '@/components/ui/spinner'
import { RelativeTime } from '@/components/ui/relative-time'
import { releaseFinishedPlan } from '@/lib/commands'
import { api, type Api } from '@/lib/api'
import { formatBytes } from '@/lib/format'
import { cn } from '@/lib/utils'
import { rootCommand, useSysbox } from '@/lib/use-sysbox'
import { memberName, resolveMember } from '@/lib/member-name'
import type { DiskUsage } from '@/lib/types'
import { workspaceRuns } from '@/routes/board/selectors'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { useStore, type RootState } from '@/store'
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
    `Scratch ${knownBytes(disk.scratch_bytes)}`,
    `Other ${knownBytes(disk.other_bytes)}`,
  ]
}

function knownBytes(bytes: number | undefined): string {
  return bytes === undefined ? 'unknown' : formatBytes(bytes)
}

export function ServerSection({ client = api }: { client?: Api } = {}) {
  const info = useStore((s) => s.info)
  const admin = useIsAdmin()
  const update = useUpdateNotice(true)
  const { server, client: cli } = useUpdateTargets()
  const packaged = useStore((s) => s.update?.cli.packaged)
  const openUpdates = useStore((s) => s.setUpdatesOpen)
  const clearDismissed = useStore((s) => s.clearDismissedUpdates)
  const checkForUpdates = useCheckForUpdates(client)
  const disk = info?.disk
  if (!info) return null
  const failed = [server, cli].find((t) => t.state === 'check_failed')
  const current = server.state === 'up_to_date' ? server : cli.state === 'up_to_date' ? cli : null
  const checking = server.state === 'checking' || cli.state === 'checking'
  const checkedLine =
    current && !update && checkedTime(current.checkedAt) ? `Up to date. Checked ${checkedTime(current.checkedAt)}.` : ''
  return (
    <SettingsSection title="Server">
      <SettingRow
        label="Version"
        help={[
          `Aether ${info.server_version}, protocol ${info.protocol_version}.`,
          update && `${update.text}.`,
          checking ? 'Checking for updates…' : checkedLine,
          packaged && packagedLine(packaged),
        ].filter(Boolean).join(' ')}
        control={
          <div className="flex flex-wrap gap-1">
            {update?.action && (
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
            <Button size="sm" variant="secondary" disabled={checking} onClick={checkForUpdates}>
              {failed ? 'Check now' : 'Check for updates…'}
            </Button>
          </div>
        }
      >
        {failed?.state === 'check_failed' && (
          <div className="space-y-1">
            <p className="text-ui-sm text-muted">
              {failed !== server
                ? 'This computer could not check for a new release:'
                : failed.unread
                  ? "Could not read the server's update status:"
                  : 'The server could not check for a new release:'}
            </p>
            <p className={cn(verbatim, 'text-state-failed')}>{failed.error}</p>
          </div>
        )}
      </SettingRow>
      <DockerInRunsRow client={client} />
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
      {disk && (disk.containers_measured_at || disk.containers_error) && <ContainerSizes disk={disk} />}
      {disk?.entries && <StorageOwners disk={disk} />}
      {admin && <RetainedContainersRow />}
    </SettingsSection>
  )
}

function DockerInRunsRow({ client }: { client: Api }) {
  const { status, error, justInstalled, install } = useSysbox(client)
  if (!status) return null
  const { state, reason, log } = status
  const installing = state === 'installing'
  const command = rootCommand(status)
  const states = {
    on: ['On', 'Agents can run docker, compose and buildx in their runs.'],
    off: ['Off', reason || 'Lets agents build and run containers inside their own runs, isolated from this server. Installs the Sysbox runtime.'],
    unsupported: ['Unavailable on this host', reason],
    installing: ['Installing', undefined],
    failed: ['Install failed', reason],
  } as const
  const [title, detail] = states[state]
  return (
    <SettingRow
      label="Docker inside runs"
      help={(
        <>
          <p className="text-text">{title}</p>
          {detail && <p className="whitespace-pre-wrap break-words">{detail}</p>}
        </>
      )}
      control={((state === 'off' && !command) || state === 'failed') && (
        <Button size="sm" variant="secondary" onClick={install}>
          {state === 'failed' ? 'Try again' : 'Install'}
        </Button>
      )}
    >
      <p role="status" className="flex items-center gap-2 text-ui-sm text-muted empty:hidden">
        {installing && <><Spinner />Installing… this takes a minute or two</>}
        {justInstalled && state === 'on' && 'New runs get it now. Reopen your Environment to use it there.'}
      </p>
      {command && <CopyableCommand command={command} />}
      {error && <p role="alert" className="whitespace-pre-wrap break-words text-ui-sm text-state-failed">{error}</p>}
      {state === 'failed' && log && (
        <details className="min-w-0 text-ui-sm text-muted">
          <summary className="cursor-pointer py-1">Show output</summary>
          <CodeBlock className="max-h-48 overflow-auto whitespace-pre-wrap break-words">{log}</CodeBlock>
        </details>
      )}
    </SettingRow>
  )
}

function ownerName(s: Pick<RootState, 'members' | 'mergedInto'>, id: string): string | undefined {
  return resolveMember(s, id).member ? memberName(s, id) : undefined
}

function ContainerSizes({ disk }: { disk: DiskUsage }) {
  const runs = useStore((s) => s.runs)
  const members = useStore((s) => s.members)
  const mergedInto = useStore((s) => s.mergedInto)
  const containers = disk.containers ?? []
  const total = containers.reduce((sum, c) => sum + c.bytes, 0)
  return (
    <SettingRow
      label="Container sizes"
      help="Files written inside a container outside its mounted home, checkout and cache, such as /tmp. They are deleted with the container and are not counted in the categories above."
    >
      <details className="min-w-0 text-ui-sm text-muted">
        <summary className="cursor-pointer py-2">
          {disk.containers_measured_at
            ? <>{containers.length} {containers.length === 1 ? 'container holds' : 'containers hold'} {formatBytes(total)} · measured <RelativeTime at={disk.containers_measured_at} /></>
            : 'Not measured yet'}
        </summary>
        <ul className="flex min-w-0 flex-col gap-1">
          {containers.map((c) => {
            const name = c.owner_kind === 'run' ? runs[c.owner_id]?.title : ownerName({ members, mergedInto }, c.owner_id)
            return (
              <li key={`${c.owner_kind}:${c.owner_id}`} className="min-w-0 break-words">
                <span className="font-medium text-text">{c.owner_kind === 'run' ? 'run' : 'environment'}{name ? ` ${name}` : ''} ({c.owner_id})</span> · {formatBytes(c.bytes)}
              </li>
            )
          })}
        </ul>
      </details>
      {disk.containers_error && <p className="whitespace-pre-wrap break-words text-ui-sm text-muted">{disk.containers_error}</p>}
    </SettingRow>
  )
}

function StorageOwners({ disk }: { disk: DiskUsage }) {
  const runs = useStore((s) => s.runs)
  const members = useStore((s) => s.members)
  const mergedInto = useStore((s) => s.mergedInto)
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
                ? ownerName({ members, mergedInto }, id)
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
