import type { ReactNode } from 'react'
import { ProgressBar } from '@/components/ui/progress-bar'
import { formatBytes } from '@/lib/format'
import type { DiskUsage, SystemContainer } from '@/lib/types'
import { busyPercent, percentText, rateText, uptimeText, type Point } from '@/routes/insights/system/model'
import { Sparkline } from '@/routes/insights/system/sparkline'
import type { SystemStats } from '@/routes/insights/system/use-system-stats'
import { cn } from '@/lib/utils'

function Tile({ label, value, compact = false, spark, children }: { label: string; value: string; compact?: boolean; spark?: ReactNode; children: ReactNode }) {
  return (
    <div role="group" aria-label={label} className="flex min-w-0 flex-col gap-1 rounded-panel border border-seam p-3">
      <h3 className="text-ui-sm text-muted">{label}</h3>
      <p className={cn('tabular-nums', compact ? 'text-prose font-medium' : 'text-title')}>{value}</p>
      <div className="min-h-9 text-ui-sm text-muted">{children}</div>
      {spark}
    </div>
  )
}

function Spark({ points, max }: { points: Point[]; max?: number }) {
  return <Sparkline points={points} max={max} className="h-6 w-full text-icon-faint" color="currentColor" />
}

export function Kpis({ stats, samples, disk }: { stats: SystemStats; samples: SystemStats['samples']; disk: DiskUsage | undefined }) {
  const { host } = stats
  const last = stats.samples.at(-1)
  const series = (pick: (s: SystemStats['samples'][number]) => number): Point[] => samples.map((s) => ({ t: s.t_ms, v: pick(s) }))
  const pressure = last?.pressure_cpu !== undefined && last.pressure_memory !== undefined && last.pressure_io !== undefined ? last : undefined
  const count = (kind: SystemContainer['owner_kind']) => stats.containers.filter((c) => c.owner_kind === kind).length
  const runs = count('run')
  const browsers = count('browser')
  const environments = count('member')
  const diskTotal = disk?.total_bytes ?? 0
  return (
    <div className="grid grid-cols-1 gap-2 min-[30rem]:grid-cols-2 lg:grid-cols-3">
      <Tile label="CPU" value={last ? percentText(busyPercent(last)) : '—'} spark={<Spark points={series(busyPercent)} max={100} />}>
        {last && `${host.cpu_cores} cores · load ${last.load1.toFixed(2)}`}
      </Tile>
      <Tile
        label="Memory"
        value={last ? percentText((last.memory_used_bytes / host.memory_total_bytes) * 100) : '—'}
        spark={<Spark points={series((s) => s.memory_used_bytes)} max={host.memory_total_bytes} />}
      >
        {last && `${formatBytes(last.memory_used_bytes)} of ${formatBytes(host.memory_total_bytes)} · swap ${formatBytes(last.swap_used_bytes)} of ${formatBytes(host.swap_total_bytes)}`}
      </Tile>
      <Tile
        label="Disk"
        value={disk && diskTotal > 0 ? percentText((disk.used_bytes / diskTotal) * 100) : '—'}
        spark={disk && diskTotal > 0 ? <ProgressBar role="meter" aria-label="Data filesystem used" value={disk.used_bytes} max={diskTotal} fillClassName="bg-icon-faint" /> : undefined}
      >
        {disk && diskTotal > 0 ? `${formatBytes(disk.used_bytes)} of ${formatBytes(diskTotal)} · data filesystem` : 'Data filesystem usage unavailable'}
      </Tile>
      <Tile
        label="Network"
        value={last ? `↓ ${rateText(last.net_rx_bps)} ↑ ${rateText(last.net_tx_bps)}` : '—'}
        spark={<Spark points={series((s) => s.net_rx_bps + s.net_tx_bps)} />}
      >
        Physical interfaces
      </Tile>
      <Tile label="Containers" value={stats.containersAt === 0 ? '—' : `${stats.containers.length} running`}>
        {stats.containersAt === 0
          ? 'Measuring containers…'
          : [
              `${runs} ${runs === 1 ? 'run' : 'runs'}`,
              browsers > 0 && `${browsers} ${browsers === 1 ? 'browser' : 'browsers'}`,
              `${environments} ${environments === 1 ? 'environment' : 'environments'}`,
              `up ${uptimeText(host.uptime_seconds)}`,
            ].filter(Boolean).join(' · ')}
      </Tile>
      {pressure && (
        <Tile
          label="Pressure"
          compact
          value={`CPU ${percentText(pressure.pressure_cpu!)} · Memory ${percentText(pressure.pressure_memory!)} · I/O ${percentText(pressure.pressure_io!)}`}
          spark={<Spark points={series((s) => Math.max(s.pressure_cpu ?? 0, s.pressure_memory ?? 0, s.pressure_io ?? 0))} max={100} />}
        >
          Share of the last 10 s some task waited
        </Tile>
      )}
    </div>
  )
}
