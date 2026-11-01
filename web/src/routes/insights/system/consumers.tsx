import { useRef, useState } from 'react'
import { Cell, Pie, PieChart, Tooltip } from 'recharts'
import { StatusDot } from '@/components/ui/status-dot'
import { ChartContainer, ChartTooltipContent } from '@/components/ui/chart'
import { Button } from '@/components/ui/button'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Callout } from '@/components/ui/callout'
import { formatBytes } from '@/lib/format'
import { presentRun, stateLabel, stateTone } from '@/lib/status'
import type { SystemContainer } from '@/lib/types'
import { cn } from '@/lib/utils'
import { Section } from '@/routes/insights/sections'
import {
  breakdown,
  capacityOf,
  containerKey,
  coresText,
  limitLevel,
  limitShare,
  metricLimit,
  metricValue,
  othersValue,
  percentText,
  ranked,
  throttledAbove,
  type Metric,
  type Slice,
  type Slots,
} from '@/routes/insights/system/model'
import { useNames } from '@/routes/insights/system/names'
import { Sparkline } from '@/routes/insights/system/sparkline'
import type { SystemStats } from '@/routes/insights/system/use-system-stats'
import { useStore } from '@/store'
import { useStateContext } from '@/store/hooks'

const metrics: { id: Metric; label: string }[] = [
  { id: 'cpu', label: 'CPU' },
  { id: 'memory', label: 'Memory' },
]

function amount(metric: Metric, value: number): string {
  return metric === 'cpu' ? `${coresText(value)} cores` : formatBytes(value)
}

function sliceColor(slice: Slice, slots: Slots): string {
  if (slice.kind === 'run') return `var(--series-${slots.get(slice.key)! + 1})`
  return { other: 'var(--series-other)', rest: 'var(--series-rest)', free: 'var(--chrome)', run: '' }[slice.kind]
}

function MetricToggle({ metric, onChange }: { metric: Metric; onChange: (m: Metric) => void }) {
  return (
    <Tabs value={metric} onValueChange={(value) => onChange(value as Metric)}>
      <TabsList look="segmented" aria-label="Resource" className="self-start">
        {metrics.map((m) => <TabsTrigger key={m.id} value={m.id}>{m.label}</TabsTrigger>)}
      </TabsList>
    </Tabs>
  )
}

export function Consumers({ stats }: { stats: SystemStats }) {
  const [metric, setMetric] = useState<Metric>('cpu')
  const [picked, setPicked] = useState('')
  const names = useNames()
  const slotsRef = useRef<Slots>(new Map())
  const last = stats.samples.at(-1)
  const capacity = capacityOf(stats.host, last, metric)
  const { containers } = stats
  // The slot map lives in a ref so a run keeps its color across polls and metric switches.
  const result = breakdown(containers, capacity, othersValue(stats.others, metric), metric, slotsRef.current, names.container)
  slotsRef.current = result.slots
  const selected = result.slices.find((s) => s.key === picked && (s.kind === 'run' || s.kind === 'other'))
  const toggle = (slice: Slice) => {
    if (slice.kind === 'run' || slice.kind === 'other') setPicked(selected?.key === slice.key ? '' : slice.key)
  }
  const rows = ranked(containers, metric)
  const shown = selected ? rows.filter((c) => selected.containers.some((s) => containerKey(s) === containerKey(c))) : rows
  const used = capacity.capacity > 0 ? (capacity.used / capacity.capacity) * 100 : 0

  return (
    <Section title="Resources by session">
      <div className="flex min-w-0 flex-col gap-4 rounded-panel border border-seam p-3 sm:p-4">
        <MetricToggle metric={metric} onChange={setMetric} />
        <div className="grid min-w-0 grid-cols-1 gap-6 md:grid-cols-2">
          <div className="flex min-w-0 flex-col gap-3">
            <div className="relative">
              <ChartContainer height={240}>
                <PieChart>
                  <Pie
                    data={result.slices}
                    dataKey="value"
                    nameKey="label"
                    innerRadius="64%"
                    outerRadius="92%"
                    startAngle={90}
                    endAngle={-270}
                    stroke="var(--canvas)"
                    strokeWidth={1}
                    isAnimationActive={false}
                    onClick={(_, index) => toggle(result.slices[index]!)}
                  >
                    {result.slices.map((slice) => (
                      <Cell
                        key={slice.key}
                        fill={sliceColor(slice, result.slots)}
                        fillOpacity={selected && selected.key !== slice.key ? 0.3 : 1}
                        cursor={slice.kind === 'run' || slice.kind === 'other' ? 'pointer' : 'default'}
                      />
                    ))}
                  </Pie>
                  <Tooltip content={<ChartTooltipContent formatValue={(v) => amount(metric, v)} />} />
                </PieChart>
              </ChartContainer>
              <div className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center text-center">
                <p className="text-title tabular-nums">{percentText(used)} used</p>
                <p className="text-ui-sm text-muted">
                  {metric === 'cpu' ? `${coresText(capacity.used)} of ${stats.host.cpu_cores} cores` : `${formatBytes(capacity.used)} of ${formatBytes(capacity.capacity)}`}
                </p>
              </div>
            </div>
            <ul aria-label="Slices" className="flex min-w-0 flex-wrap justify-center gap-x-1 gap-y-0.5">
              {result.slices.map((slice) => {
                const content = (
                  <>
                    <span aria-hidden className="size-2.5 shrink-0 rounded-[3px] border border-seam" style={{ background: sliceColor(slice, result.slots) }} />
                    <span className="min-w-0 max-w-48 truncate text-left">{slice.label}</span>
                    <span className="shrink-0 text-muted tabular-nums">{amount(metric, slice.value)}</span>
                  </>
                )
                const selectable = slice.kind === 'run' || slice.kind === 'other'
                return (
                  <li key={slice.key} className="min-w-0 max-w-full">
                    {selectable ? (
                      <Button
                        variant="ghost"
                        aria-pressed={selected?.key === slice.key}
                        onClick={() => toggle(slice)}
                        className={cn(
                          'h-auto max-w-full min-w-0 justify-start gap-1.5 px-1.5 py-1 text-ui-sm font-normal text-text',
                          selected?.key === slice.key && 'bg-accent-soft',
                        )}
                      >
                        {content}
                      </Button>
                    ) : (
                      <div className="flex min-w-0 items-center gap-1.5 px-1.5 py-1 text-ui-sm">{content}</div>
                    )}
                  </li>
                )
              })}
            </ul>
          </div>
          <div className="flex min-w-0 flex-col gap-2">
            {selected && (
              <p className="flex flex-wrap items-center gap-x-2 text-ui-sm text-muted">
                <span>{selected.kind === 'other' ? `Showing ${selected.containers.length} ${selected.containers.length === 1 ? 'session' : 'sessions'} in ${selected.label}` : `Showing ${selected.label}`}</span>
                <Button variant="link" onClick={() => setPicked('')}>Show all</Button>
              </p>
            )}
            <Rows stats={stats} metric={metric} rows={shown} result={result} capacity={capacity.capacity} />
            {!selected && containers.length > 0 && <EverythingElse rest={result.rest} stats={stats} metric={metric} />}
          </div>
        </div>
      </div>
    </Section>
  )
}

function Rows({ stats, metric, rows, result, capacity }: { stats: SystemStats; metric: Metric; rows: SystemContainer[]; result: ReturnType<typeof breakdown>; capacity: number }) {
  const names = useNames()
  const ctx = useStateContext()
  const navigate = useStore((s) => s.navigate)
  const selfID = useStore((s) => s.info?.member?.id)
  if (stats.containersError && stats.containers.length === 0) {
    return <Callout tone="failed" role="alert" className="whitespace-pre-wrap">{stats.containersError}</Callout>
  }
  if (stats.containersAt === 0) return <p className="text-ui text-muted">Measuring containers…</p>
  if (stats.containers.length === 0) return <p className="text-ui text-muted">No run containers are running.</p>
  return (
    <>
      {stats.containersError && <p className="whitespace-pre-wrap break-words text-ui-sm text-state-failed">{stats.containersError}</p>}
      <ul aria-label="Containers by usage" className="flex min-w-0 flex-col divide-y divide-seam">
        {rows.map((c) => {
          const key = containerKey(c)
          const slot = result.slots.get(key)
          const color = slot !== undefined && !result.folded.has(key) ? `var(--series-${slot + 1})` : 'var(--series-other)'
          const run = c.owner_kind === 'member' ? undefined : names.runs[c.owner_id]
          const present = run ? presentRun(run, ctx) : undefined
          const value = metricValue(c, metric)
          const limit = metricLimit(c, metric)
          const share = limitShare(c, metric)
          const level = limitLevel(share)
          const history = stats.history.get(key)?.[metric] ?? []
          const hostShare = capacity > 0 ? percentText((value / capacity) * 100) : ''
          const sub = c.owner_kind === 'member' ? 'Environment container' : names.member(c.member_id)
          // The Environment page opens only the viewer's own environment.
          const open = c.owner_kind !== 'member' ? () => navigate('run', { runId: c.owner_id }) : c.owner_id === selfID ? () => navigate('environment') : undefined
          const rowClass = 'flex h-auto w-full min-w-0 items-center justify-start gap-3 whitespace-normal px-1.5 py-2 text-left font-normal text-text'
          const content = (
              <>
                <span aria-hidden className="size-2.5 shrink-0 rounded-[3px]" style={{ background: color }} />
                <span className="flex min-w-0 flex-1 flex-col">
                  <span className="truncate text-ui font-medium text-text">{names.container(c)}</span>
                  <span className="flex min-w-0 items-center gap-1.5 text-ui-sm text-muted">
                    <span className="truncate">{sub}</span>
                    {(present || c.status) && (
                      <>
                        <span aria-hidden>·</span>
                        <StatusDot tone={present ? stateTone[present.state] : 'neutral'} />
                        <span className="truncate">{present ? stateLabel[present.state] : c.status}</span>
                      </>
                    )}
                  </span>
                </span>
                <Sparkline points={history} max={limit > 0 ? limit : undefined} color={color} className="hidden h-6 w-16 shrink-0 sm:block" />
                <span className="flex w-36 shrink-0 flex-col items-end gap-1 text-ui-sm">
                  <span className="text-text tabular-nums">
                    {metric === 'cpu' && c.cpu_cores === undefined
                      ? 'Measuring…'
                      : metric === 'cpu'
                        ? `${coresText(value)} cores · ${hostShare} of host`
                        : limit > 0
                          ? `${formatBytes(value)} of ${formatBytes(limit)} limit`
                          : `${formatBytes(value)} · ${hostShare} of host`}
                  </span>
                  {share !== undefined && (
                    <span aria-hidden className="block h-1 w-full overflow-hidden rounded-full bg-chrome">
                      <span
                        className={cn('block h-full', level === 'critical' ? 'bg-state-failed' : level === 'near' ? 'bg-state-needs-you' : 'bg-icon-faint')}
                        style={{ width: `${Math.min(100, share * 100)}%` }}
                      />
                    </span>
                  )}
                  {level && <span className={level === 'critical' ? 'text-state-failed' : 'text-state-needs-you'}>near limit</span>}
                  {metric === 'cpu' && (c.cpu_throttled_percent ?? 0) > throttledAbove && (
                    <span className="text-muted">throttled {Math.round(c.cpu_throttled_percent!)}%</span>
                  )}
                </span>
              </>
          )
          return (
            <li key={key}>
              {open ? <Button variant="ghost" onClick={open} className={rowClass}>{content}</Button> : <div className={cn(rowClass, 'text-ui')}>{content}</div>}
            </li>
          )
        })}
      </ul>
    </>
  )
}

function EverythingElse({ rest, stats, metric }: { rest: number; stats: SystemStats; metric: Metric }) {
  const others = othersValue(stats.others, metric)
  const host = Math.max(0, rest - others)
  return (
    <div className="mt-1 flex min-w-0 flex-col gap-0.5 border-t border-seam px-1.5 pt-2 text-ui-sm text-muted">
      <p className="flex justify-between gap-2 font-medium text-text">
        <span>Everything else</span>
        <span className="tabular-nums">{amount(metric, rest)}</span>
      </p>
      {stats.others && (
        <p className="flex justify-between gap-2">
          <span>Other containers ({stats.others.containers})</span>
          <span className="tabular-nums">{amount(metric, others)}</span>
        </p>
      )}
      <p className="flex justify-between gap-2">
        <span>Aether server, Docker and host processes</span>
        <span className="tabular-nums">{amount(metric, host)}</span>
      </p>
    </div>
  )
}
