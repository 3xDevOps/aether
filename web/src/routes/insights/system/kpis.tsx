import { ProgressBar } from '@/components/ui/progress-bar'
import { StatTile } from '@/components/ui/stat-tile'
import { formatBytes } from '@/lib/format'
import type { DiskUsage, SystemContainer } from '@/lib/types'
import { useTween } from '@/lib/use-tween'
import { busyPercent, percentText, rateText, uptimeText, type Point } from '@/routes/insights/system/model'
import { Sparkline } from '@/routes/insights/system/sparkline'
import type { SystemStats } from '@/routes/insights/system/use-system-stats'

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
  const cpu = useTween(last && busyPercent(last))
  const memory = useTween(last && (last.memory_used_bytes / host.memory_total_bytes) * 100)
  const received = useTween(last?.net_rx_bps)
  const sent = useTween(last?.net_tx_bps)
  return (
    <div className="grid grid-cols-1 gap-3 min-[30rem]:grid-cols-2 lg:grid-cols-3">
      <StatTile role="group" label="CPU" value={cpu === undefined ? '—' : percentText(cpu)} spark={<Spark points={series(busyPercent)} max={100} />}>
        {last && `${host.cpu_cores} cores · load ${last.load1.toFixed(2)}`}
      </StatTile>
      <StatTile
        role="group"
        label="Memory"
        value={memory === undefined ? '—' : percentText(memory)}
        spark={<Spark points={series((s) => s.memory_used_bytes)} max={host.memory_total_bytes} />}
      >
        {last && `${formatBytes(last.memory_used_bytes)} of ${formatBytes(host.memory_total_bytes)} · swap ${formatBytes(last.swap_used_bytes)} of ${formatBytes(host.swap_total_bytes)}`}
      </StatTile>
      <StatTile
        role="group"
        label="Disk"
        value={disk && diskTotal > 0 ? percentText((disk.used_bytes / diskTotal) * 100) : '—'}
        spark={disk && diskTotal > 0 ? <ProgressBar role="meter" aria-label="Data filesystem used" value={disk.used_bytes} max={diskTotal} fillClassName="bg-icon-faint" /> : undefined}
      >
        {disk && diskTotal > 0 ? `${formatBytes(disk.used_bytes)} of ${formatBytes(diskTotal)} · data filesystem` : 'Data filesystem usage unavailable'}
      </StatTile>
      <StatTile
        role="group"
        label="Network"
        value={received === undefined || sent === undefined ? '—' : `↓ ${rateText(received)} ↑ ${rateText(sent)}`}
        spark={<Spark points={series((s) => s.net_rx_bps + s.net_tx_bps)} />}
      >
        Physical interfaces
      </StatTile>
      <StatTile role="group" label="Containers" value={stats.containersAt === 0 ? '—' : `${stats.containers.length} running`}>
        {stats.containersAt === 0
          ? 'Measuring containers…'
          : [
              `${runs} ${runs === 1 ? 'session' : 'sessions'}`,
              browsers > 0 && `${browsers} ${browsers === 1 ? 'browser' : 'browsers'}`,
              `${environments} ${environments === 1 ? 'environment' : 'environments'}`,
              `up ${uptimeText(host.uptime_seconds)}`,
            ].filter(Boolean).join(' · ')}
      </StatTile>
      {pressure && (
        <StatTile
        role="group"
          label="Pressure"
          compact
          value={`CPU ${percentText(pressure.pressure_cpu!)} · Memory ${percentText(pressure.pressure_memory!)} · I/O ${percentText(pressure.pressure_io!)}`}
          spark={<Spark points={series((s) => Math.max(s.pressure_cpu ?? 0, s.pressure_memory ?? 0, s.pressure_io ?? 0))} max={100} />}
        >
          Share of the last 10 s some task waited
        </StatTile>
      )}
    </div>
  )
}
