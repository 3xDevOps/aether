import type { ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { RelativeTime } from '@/components/ui/relative-time'
import { SectionLabel } from '@/components/ui/section-label'
import { formatBytes } from '@/lib/format'
import type { DiskUsage } from '@/lib/types'
import { Section } from '@/routes/insights/sections'
import { useNames } from '@/routes/insights/system/names'
import { useStore } from '@/store'

function known(bytes: number | undefined): string {
  return bytes === undefined ? 'unknown' : formatBytes(bytes)
}

function Block({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <SectionLabel as="h3">{title}</SectionLabel>
      {children}
    </div>
  )
}

function categories(disk: DiskUsage): { name: string; bytes?: number }[] {
  return [
    { name: 'Worktrees', bytes: disk.worktree_bytes },
    { name: 'Transcripts', bytes: disk.transcript_bytes },
    { name: 'Database', bytes: disk.database_bytes },
    ...(disk.repo_bytes === undefined ? [] : [{ name: 'Repositories', bytes: disk.repo_bytes }]),
    { name: 'Homes', bytes: disk.home_bytes },
    { name: 'Caches', bytes: disk.cache_bytes },
    { name: 'Evidence', bytes: disk.evidence_bytes },
    { name: 'Scratch', bytes: disk.scratch_bytes },
    { name: 'Other', bytes: disk.other_bytes },
  ].sort((a, b) => (b.bytes ?? -1) - (a.bytes ?? -1))
}

function BarList({ items }: { items: { name: string; bytes?: number }[] }) {
  const max = Math.max(1, ...items.map((i) => i.bytes ?? 0))
  return (
    <ul aria-label="Aether data" className="flex min-w-0 flex-col gap-1">
      {items.map((item) => (
        <li key={item.name} className="relative flex h-7 min-w-0 items-center justify-between gap-3 px-2 text-ui-sm">
          <span aria-hidden className="absolute inset-y-0 left-0 rounded-control bg-series-1 opacity-25" style={{ width: `${((item.bytes ?? 0) / max) * 100}%` }} />
          <span className="relative truncate text-text">{item.name}</span>
          <span className="relative shrink-0 text-text tabular-nums">{known(item.bytes)}</span>
        </li>
      ))}
    </ul>
  )
}

function Figures({ rows }: { rows: [string, string][] }) {
  return (
    <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-0.5 text-ui-sm">
      {rows.map(([name, value]) => (
        <div key={name} className="contents">
          <dt className="text-muted">{name}</dt>
          <dd className="text-text tabular-nums">{value}</dd>
        </div>
      ))}
    </dl>
  )
}

function RowButton({ onOpen, children }: { onOpen?: () => void; children: ReactNode }) {
  const className = 'flex w-full min-w-0 flex-col gap-0.5 px-1.5 py-1 text-left text-ui-sm'
  return onOpen ? (
    <Button variant="ghost" onClick={onOpen} className={`${className} h-auto items-stretch justify-start whitespace-normal font-normal`}>{children}</Button>
  ) : (
    <div className={className}>{children}</div>
  )
}

function Docker({ docker }: { docker: DiskUsage['docker'] }) {
  if (!docker) return <p className="text-ui-sm text-muted">Docker accounting unavailable.</p>
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <Figures
        rows={[
          ['Images', known(docker.images_bytes)],
          ['Containers', known(docker.containers_bytes)],
          ['Volumes', known(docker.volumes_bytes)],
          ['Build cache', known(docker.build_cache_bytes)],
          ['Reclaimable', known(docker.reclaimable_bytes)],
          ['Docker filesystem', `${known(docker.used_bytes)} used · ${known(docker.total_bytes)} total · ${known(docker.free_bytes)} free`],
        ]}
      />
      <p className="text-ui-sm text-muted">
        These figures cover the whole Docker daemon, including workloads outside Aether; bind-backed homes count under Aether homes. Reclaimable is Docker's own unused classification, not
        permission to delete saved images.{' '}
        {docker.shared_filesystem === undefined
          ? 'Whether Docker shares the data filesystem is unknown.'
          : docker.shared_filesystem
            ? 'Docker shares the data filesystem.'
            : 'Docker uses a separate filesystem.'}
      </p>
      {docker.error && <p className="whitespace-pre-wrap break-words text-ui-sm text-muted">{docker.error}</p>}
    </div>
  )
}

function ContainerLayers({ disk }: { disk: DiskUsage }) {
  const names = useNames()
  const navigate = useStore((s) => s.navigate)
  const selfID = useStore((s) => s.info?.member?.id)
  const containers = disk.containers ?? []
  const total = containers.reduce((sum, c) => sum + c.bytes, 0)
  return (
    <Block title="Container writable layers">
      <p className="text-ui-sm text-muted">Files a container wrote outside its mounted home, checkout and cache, such as /tmp. They are deleted with the container and are not counted above.</p>
      <p className="text-ui-sm text-text">
        {disk.containers_measured_at
          ? <>{containers.length} {containers.length === 1 ? 'container holds' : 'containers hold'} {formatBytes(total)} · measured <RelativeTime at={disk.containers_measured_at} /></>
          : 'Not measured yet'}
      </p>
      <ul className="flex min-w-0 flex-col">
        {containers.map((c) => {
          const name = names.owner(c.owner_kind, c.owner_id)
          return (
            <li key={`${c.owner_kind}:${c.owner_id}`}>
              <RowButton onOpen={c.owner_kind === 'run' ? () => navigate('run', { runId: c.owner_id }) : c.owner_id === selfID ? () => navigate('environment') : undefined}>
                <span className="flex min-w-0 justify-between gap-3">
                  <span className="min-w-0 break-words font-medium text-text">{c.owner_kind === 'run' ? 'run' : 'environment'}{name ? ` ${name}` : ''} ({c.owner_id})</span>
                  <span className="shrink-0 text-muted tabular-nums">{formatBytes(c.bytes)}</span>
                </span>
              </RowButton>
            </li>
          )
        })}
      </ul>
      {disk.containers_error && <p className="whitespace-pre-wrap break-words text-ui-sm text-muted">{disk.containers_error}</p>}
    </Block>
  )
}

function Owners({ disk }: { disk: DiskUsage }) {
  const names = useNames()
  const navigate = useStore((s) => s.navigate)
  return (
    <Block title="Largest storage owners">
      <p className="text-ui-sm text-muted">Attribution overlaps the categories above; these bytes are not additional totals.</p>
      <details className="min-w-0 text-ui-sm text-muted">
        <summary className="cursor-pointer py-1">
          {(disk.entries?.length ?? 0) === 1 ? '1 owner' : `${disk.entries?.length ?? 0} owners`} shown{disk.truncated ? ' · Truncated to the largest 50 entries' : ''}
        </summary>
        <ul className="flex min-w-0 flex-col gap-1">
          {disk.entries?.map((entry, index) => {
            const id = entry.owner_id
            const name = id && names.owner(entry.owner_kind, id)
            return (
              <li key={index}>
                <RowButton onOpen={entry.owner_kind === 'run' && id ? () => navigate('run', { runId: id }) : undefined}>
                  <span className="min-w-0 break-words font-medium text-text">
                    {entry.owner_kind}{name ? ` ${name}` : ''}{id ? ` (${id})` : ''} · {entry.kind}{entry.pool ? ` (${entry.pool})` : ''} · {formatBytes(entry.bytes)}
                  </span>
                  <span>{entry.reason}</span>
                  <span>
                    Reclaimable: {known(entry.reclaimable_bytes)}
                    {entry.retained_until && <> · {entry.kind === 'cache' ? 'Age target' : 'Retained until'} <time dateTime={entry.retained_until}>{new Date(entry.retained_until).toLocaleString()}</time></>}
                  </span>
                  {entry.error && <span className="whitespace-pre-wrap">{entry.error}</span>}
                </RowButton>
              </li>
            )
          })}
        </ul>
      </details>
    </Block>
  )
}

export function StorageSection() {
  const disk = useStore((s) => s.info?.disk)
  const diskError = useStore((s) => s.info?.diskError)
  if (!disk) {
    return (
      <Section title="Storage">
        <p className="text-ui-sm text-muted">Storage accounting unavailable.</p>
        {diskError && <p className="whitespace-pre-wrap break-words text-ui-sm text-muted">{diskError}</p>}
      </Section>
    )
  }
  return (
    <Section title="Storage">
      <div className="flex min-w-0 flex-col gap-6 rounded-panel border border-seam p-3 sm:p-4">
        <div role="group" aria-label="Disk usage" className="flex min-w-0 flex-col gap-1.5">
          <p className="text-ui text-text">
            {disk.total_bytes > 0
              ? `${formatBytes(disk.used_bytes)} of ${formatBytes(disk.total_bytes)} used on the filesystem holding the data directory, ${formatBytes(disk.free_bytes)} free for an unprivileged writer.`
              : 'Data filesystem usage unavailable.'}
          </p>
          {disk.total_bytes > 0 && (
            <span role="meter" aria-label="Disk used" aria-valuemin={0} aria-valuemax={disk.total_bytes} aria-valuenow={disk.used_bytes} className="block h-1.5 overflow-hidden rounded-full bg-chrome">
              <span className="block h-full bg-icon-faint" style={{ width: `${Math.min(100, (disk.used_bytes / disk.total_bytes) * 100)}%` }} />
            </span>
          )}
        </div>
        <Block title="Aether data">
          <BarList items={categories(disk)} />
          <p className="text-ui-sm text-muted">
            Snapshots {known(disk.snapshot_bytes)} (included in worktrees). These categories attribute Aether data, not the whole filesystem or guaranteed reclaimable space. Run and terminal pools
            belong to the launching member. Automatic cleanup protects active, retained and uncertain owners.
          </p>
          {disk.warnings && disk.warnings.length > 0 && (
            <div className="text-ui-sm text-muted">
              <p>Partial measurement; affected categories may be incomplete.</p>
              <ul className="list-inside list-disc">
                {disk.warnings.map((warning, index) => <li key={index} className="whitespace-pre-wrap break-words">{warning}</li>)}
              </ul>
            </div>
          )}
        </Block>
        <Block title="Docker storage">
          <Docker docker={disk.docker} />
        </Block>
        {(disk.containers_measured_at || disk.containers_error) && <ContainerLayers disk={disk} />}
        {disk.entries && <Owners disk={disk} />}
      </div>
    </Section>
  )
}
