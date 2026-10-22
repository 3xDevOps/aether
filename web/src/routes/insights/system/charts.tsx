import { Area, AreaChart, CartesianGrid, Line, LineChart, ReferenceLine, Tooltip, XAxis, YAxis } from 'recharts'
import { ChartContainer, ChartLegend, ChartTooltipContent, type ChartSeries } from '@/components/ui/chart'
import { formatBytes } from '@/lib/format'
import type { SystemSample } from '@/lib/types'
import { Section } from '@/routes/insights/sections'
import { clock, percentText, rateText } from '@/routes/insights/system/model'

interface Trace extends ChartSeries {
  key: string
}

interface Spec {
  title: string
  kind: 'area' | 'line'
  lines: Trace[]
  data: Record<string, number>[]
  format: (value: number) => string
  max?: number
  reference?: { value: number; label: string }
}

const gridProps = { vertical: false, strokeWidth: 1 }

function TimeChart({ spec, domain }: { spec: Spec; domain: [number, number] }) {
  const { kind, lines, data, format, max, reference } = spec
  const axes = (
    <>
      <CartesianGrid {...gridProps} />
      <XAxis dataKey="t" type="number" scale="time" domain={domain} tickFormatter={clock} tickCount={5} minTickGap={36} tickLine={false} />
      <YAxis domain={[0, max ?? 'auto']} tickFormatter={format} width={56} tickLine={false} axisLine={false} tickCount={4} />
      <Tooltip
        isAnimationActive={false}
        cursor={{ stroke: 'var(--control-border)', strokeWidth: 1 }}
        content={<ChartTooltipContent formatLabel={clock} formatValue={format} />}
      />
      {reference && (
        <ReferenceLine y={reference.value} ifOverflow="extendDomain" stroke="var(--text-muted)" strokeDasharray="4 3" label={{ value: reference.label, position: 'insideTopRight', fill: 'var(--text-muted)', fontSize: 11 }} />
      )}
    </>
  )
  return (
    <ChartContainer height={180}>
      {kind === 'area' ? (
        <AreaChart data={data} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
          {axes}
          {lines.map((l) => (
            <Area key={l.key} type="monotone" dataKey={l.key} name={l.name} stackId="stack" stroke={l.color} strokeWidth={2} fill={l.color} fillOpacity={0.3} isAnimationActive={false} dot={false} activeDot={{ r: 3 }} />
          ))}
        </AreaChart>
      ) : (
        <LineChart data={data} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
          {axes}
          {lines.map((l) => (
            <Line key={l.key} type="monotone" dataKey={l.key} name={l.name} stroke={l.color} strokeWidth={2} isAnimationActive={false} dot={false} activeDot={{ r: 3 }} />
          ))}
        </LineChart>
      )}
    </ChartContainer>
  )
}

function row(s: SystemSample, pick: Record<string, (s: SystemSample) => number>): Record<string, number> {
  return { t: s.t_ms, ...Object.fromEntries(Object.entries(pick).map(([key, get]) => [key, get(s)])) }
}

function specs(samples: SystemSample[], cores: number, memoryTotal: number): Spec[] {
  const count = (v: number) => (v < 10 ? v.toFixed(1) : String(Math.round(v)))
  const list: Spec[] = [
    {
      title: 'CPU',
      kind: 'area',
      max: 100,
      format: percentText,
      lines: [
        { key: 'user', name: 'User', color: 'var(--series-1)' },
        { key: 'system', name: 'System', color: 'var(--series-2)' },
        { key: 'iowait', name: 'I/O wait', color: 'var(--series-4)' },
        { key: 'steal', name: 'Steal', color: 'var(--series-8)' },
      ],
      data: samples.map((s) => row(s, { user: (x) => x.cpu_user, system: (x) => x.cpu_system, iowait: (x) => x.cpu_iowait, steal: (x) => x.cpu_steal })),
    },
    {
      title: 'Memory',
      kind: 'area',
      max: memoryTotal,
      format: formatBytes,
      lines: [
        { key: 'used', name: 'Used', color: 'var(--series-1)' },
        { key: 'cache', name: 'Cache', color: 'var(--series-4)' },
        { key: 'free', name: 'Free', color: 'var(--series-rest)' },
      ],
      data: samples.map((s) => row(s, { used: (x) => x.memory_used_bytes, cache: (x) => Math.max(0, x.memory_available_bytes - x.memory_free_bytes), free: (x) => x.memory_free_bytes })),
    },
    {
      title: 'Load average',
      kind: 'line',
      format: count,
      reference: { value: cores, label: `${cores} cores` },
      lines: [
        { key: 'load1', name: '1 min', color: 'var(--series-1)' },
        { key: 'load5', name: '5 min', color: 'var(--series-2)' },
        { key: 'load15', name: '15 min', color: 'var(--series-3)' },
      ],
      data: samples.map((s) => row(s, { load1: (x) => x.load1, load5: (x) => x.load5, load15: (x) => x.load15 })),
    },
  ]
  if (samples.at(-1)?.pressure_cpu !== undefined) {
    list.push({
      title: 'Pressure',
      kind: 'line',
      format: percentText,
      lines: [
        { key: 'cpu', name: 'CPU', color: 'var(--series-1)' },
        { key: 'memory', name: 'Memory', color: 'var(--series-2)' },
        { key: 'io', name: 'I/O', color: 'var(--series-3)' },
      ],
      data: samples.map((s) => row(s, { cpu: (x) => x.pressure_cpu ?? 0, memory: (x) => x.pressure_memory ?? 0, io: (x) => x.pressure_io ?? 0 })),
    })
  }
  list.push(
    {
      title: 'Network',
      kind: 'line',
      format: rateText,
      lines: [
        { key: 'rx', name: 'Received', color: 'var(--series-1)' },
        { key: 'tx', name: 'Sent', color: 'var(--series-2)' },
      ],
      data: samples.map((s) => row(s, { rx: (x) => x.net_rx_bps, tx: (x) => x.net_tx_bps })),
    },
    {
      title: 'Disk I/O',
      kind: 'line',
      format: rateText,
      lines: [
        { key: 'read', name: 'Read', color: 'var(--series-1)' },
        { key: 'write', name: 'Write', color: 'var(--series-2)' },
      ],
      data: samples.map((s) => row(s, { read: (x) => x.disk_read_bps, write: (x) => x.disk_write_bps })),
    },
  )
  return list
}

export function Charts({ samples, cores, memoryTotal, rangeMs }: { samples: SystemSample[]; cores: number; memoryTotal: number; rangeMs: number }) {
  const newest = samples.at(-1)?.t_ms ?? 0
  const domain: [number, number] = [newest - rangeMs, newest]
  return (
    <Section title="History">
      <div className="grid min-w-0 grid-cols-1 gap-3 lg:grid-cols-2">
        {specs(samples, cores, memoryTotal).map((spec) => (
          <figure key={spec.title} aria-label={spec.title} className="m-0 flex min-w-0 flex-col gap-2 rounded-panel border border-seam p-3">
            <figcaption className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
              <h3 className="text-ui font-medium text-text">{spec.title}</h3>
              <ChartLegend series={spec.lines} />
            </figcaption>
            <TimeChart spec={spec} domain={domain} />
          </figure>
        ))}
      </div>
    </Section>
  )
}
