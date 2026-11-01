import { formatBytes } from '@/lib/format'
import type { SystemContainer, SystemHost, SystemOthers, SystemSample } from '@/lib/types'

export type Metric = 'cpu' | 'memory'

export const maxListed = 5
/** A container below this share of host capacity folds into "Other runs". */
export const minShare = 0.02
export const historyMs = 15 * 60_000

export type SliceKind = 'run' | 'other' | 'rest' | 'free'

export interface Slice {
  key: string
  label: string
  kind: SliceKind
  value: number
  containers: SystemContainer[]
}

export type Slots = ReadonlyMap<string, number>

export function containerKey(c: Pick<SystemContainer, 'owner_kind' | 'owner_id'>): string {
  return `${c.owner_kind}:${c.owner_id}`
}

export function metricValue(c: SystemContainer, metric: Metric): number {
  return metric === 'cpu' ? c.cpu_cores ?? 0 : c.memory_bytes
}

export function metricLimit(c: SystemContainer, metric: Metric): number {
  return (metric === 'cpu' ? c.cpu_limit_cores : c.memory_limit_bytes) ?? 0
}

export function ranked(containers: SystemContainer[], metric: Metric): SystemContainer[] {
  return containers
    .map((c) => ({ c, v: metricValue(c, metric) }))
    .sort((a, b) => b.v - a.v || containerKey(a.c).localeCompare(containerKey(b.c)))
    .map(({ c }) => c)
}

export function busyPercent(s: SystemSample): number {
  return s.cpu_user + s.cpu_system + s.cpu_iowait + s.cpu_steal
}

export interface Capacity {
  capacity: number
  used: number
}

export function capacityOf(host: SystemHost, sample: SystemSample | undefined, metric: Metric): Capacity {
  const capacity = metric === 'cpu' ? host.cpu_cores : host.memory_total_bytes
  if (!sample) return { capacity, used: 0 }
  return { capacity, used: metric === 'cpu' ? (busyPercent(sample) / 100) * host.cpu_cores : sample.memory_used_bytes }
}

export const slotCount = 8

/**
 * Colors belong to runs, not ranks: a run keeps its slot while it is present, even folded
 * into Other runs. A newly shown run takes the lowest slot no present run holds; only when
 * all are held does it take one from a run that is not shown.
 */
export function assignSlots(previous: Slots, shown: string[], present: ReadonlySet<string>): Map<string, number> {
  const next = new Map([...previous].filter(([key]) => present.has(key)))
  const shownKeys = new Set(shown)
  const held = new Set<number>()
  const unplaced: string[] = []
  for (const key of shown) {
    const slot = next.get(key)
    if (slot !== undefined && !held.has(slot)) held.add(slot)
    else unplaced.push(key)
  }
  for (const key of unplaced) {
    next.delete(key)
    const kept = new Set(next.values())
    let free = 0
    while (free < slotCount && (held.has(free) || kept.has(free))) free++
    if (free === slotCount) {
      free = 0
      while (held.has(free)) free++
      for (const [other, slot] of next) if (slot === free && !shownKeys.has(other)) next.delete(other)
    }
    next.set(key, free)
    held.add(free)
  }
  return next
}

export interface Breakdown {
  slices: Slice[]
  slots: Map<string, number>
  folded: Set<string>
  /** Host use not attributed to a visible container: the Aether server, Docker, host processes and others. */
  rest: number
}

/** `others` is the use of the containers the caller cannot see; host and container figures are sampled apart, so it bounds `rest` from below. */
export function breakdown(
  containers: SystemContainer[],
  { capacity, used }: Capacity,
  others: number,
  metric: Metric,
  previous: Slots,
  label: (c: SystemContainer) => string,
): Breakdown {
  const order = ranked(containers, metric)
  const listed = order.filter((c) => capacity > 0 && metricValue(c, metric) / capacity >= minShare).slice(0, maxListed)
  const listedKeys = new Set(listed.map(containerKey))
  const remaining = order.filter((c) => !listedKeys.has(containerKey(c)))
  const slots = assignSlots(previous, [...listedKeys], new Set(order.map(containerKey)))
  const slices: Slice[] = listed.map((c) => ({
    key: containerKey(c),
    label: label(c),
    kind: 'run',
    value: metricValue(c, metric),
    containers: [c],
  }))
  if (remaining.length > 0) {
    slices.push({
      key: 'other',
      label: 'Other sessions',
      kind: 'other',
      value: remaining.reduce((sum, c) => sum + metricValue(c, metric), 0),
      containers: remaining,
    })
  }
  const attributed = slices.reduce((sum, s) => sum + s.value, 0)
  const rest = Math.max(others, used - attributed)
  slices.push({ key: 'rest', label: 'Everything else', kind: 'rest', value: rest, containers: [] })
  slices.push({ key: 'free', label: 'Free', kind: 'free', value: Math.max(0, capacity - attributed - rest), containers: [] })
  return { slices, slots, folded: new Set(remaining.map(containerKey)), rest }
}

export function othersValue(others: SystemOthers | undefined, metric: Metric): number {
  return others ? (metric === 'cpu' ? others.cpu_cores : others.memory_bytes) : 0
}

export function limitShare(c: SystemContainer, metric: Metric): number | undefined {
  const limit = metricLimit(c, metric)
  return limit > 0 ? metricValue(c, metric) / limit : undefined
}

export type LimitLevel = 'near' | 'critical'

export function limitLevel(share: number | undefined): LimitLevel | undefined {
  if (share === undefined) return undefined
  if (share >= 0.95) return 'critical'
  return share >= 0.8 ? 'near' : undefined
}

export const throttledAbove = 5

export type Point = { t: number; v: number }
export type History = ReadonlyMap<string, { cpu: Point[]; memory: Point[] }>

export function appendHistory(previous: History, containers: SystemContainer[], at: number): History {
  const next = new Map<string, { cpu: Point[]; memory: Point[] }>()
  const floor = at - historyMs
  for (const c of containers) {
    const key = containerKey(c)
    const old = previous.get(key)
    const cpu = (old?.cpu ?? []).filter((p) => p.t >= floor && p.t < at)
    const memory = (old?.memory ?? []).filter((p) => p.t >= floor && p.t < at)
    if (c.cpu_cores !== undefined) cpu.push({ t: at, v: c.cpu_cores })
    memory.push({ t: at, v: c.memory_bytes })
    next.set(key, { cpu, memory })
  }
  return next
}

export function mergeSamples(buffer: SystemSample[], incoming: SystemSample[], retentionMs: number): SystemSample[] {
  const last = buffer.at(-1)?.t_ms ?? 0
  const merged = [...buffer, ...incoming.filter((s) => s.t_ms > last)]
  const newest = merged.at(-1)
  if (!newest) return merged
  const floor = newest.t_ms - retentionMs
  const first = merged.findIndex((s) => s.t_ms >= floor)
  return first <= 0 ? merged : merged.slice(first)
}

export const ranges = [
  { id: '5m', ms: 5 * 60_000 },
  { id: '15m', ms: 15 * 60_000 },
  { id: '1h', ms: 60 * 60_000 },
] as const

export type RangeID = (typeof ranges)[number]['id']

export function inRange(samples: SystemSample[], ms: number): SystemSample[] {
  const newest = samples.at(-1)
  return newest ? samples.filter((s) => s.t_ms >= newest.t_ms - ms) : samples
}

export function percentText(value: number): string {
  return `${value < 10 ? value.toFixed(1) : Math.round(value)}%`
}

export function coresText(value: number): string {
  return value < 10 ? value.toFixed(2) : value.toFixed(1)
}

export function rateText(bps: number): string {
  return `${formatBytes(bps)}/s`
}

export function uptimeText(seconds: number): string {
  const days = Math.floor(seconds / 86_400)
  const hours = Math.floor((seconds % 86_400) / 3600)
  if (days > 0) return `${days}d ${hours}h`
  const minutes = Math.floor((seconds % 3600) / 60)
  return hours > 0 ? `${hours}h ${minutes}m` : `${minutes}m`
}

export function clock(t: number): string {
  return new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false })
}
