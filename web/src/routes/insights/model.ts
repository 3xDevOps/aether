import type { Tone } from '@/components/ui/status-dot'
import { outcomeState, stateLabel, stateTone } from '@/lib/status'
import type { InsightsOutcome, InsightsResult, InsightsRun } from '@/lib/types'

/** The last seven local days, ending at the next local midnight so today counts. */
export function weekRange(now: Date): { from: Date; to: Date } {
  const to = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1)
  return { from: new Date(to.getFullYear(), to.getMonth(), to.getDate() - 7), to }
}

export function rfc3339(date: Date): string {
  return date.toISOString().replace(/\.\d{3}Z$/, 'Z')
}

export function plural(n: number, one: string, many = `${one}s`): string {
  return `${n.toLocaleString()} ${n === 1 ? one : many}`
}

export function duration(seconds: number): string {
  const s = Math.max(0, Math.round(seconds))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 10 && s % 60) return `${m}m ${s % 60}s`
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return m % 60 ? `${h}h ${m % 60}m` : `${h}h`
  const d = Math.floor(h / 24)
  return h % 24 ? `${d}d ${h % 24}h` : `${d}d`
}

/** Dates on the wire are local calendar days; reading them as UTC keeps the day. */
export function calendarDay(date: string): Date {
  return new Date(`${date}T00:00:00Z`)
}

export function dayLabel(date: string, options: Intl.DateTimeFormatOptions = { month: 'short', day: 'numeric' }): string {
  return calendarDay(date).toLocaleDateString(undefined, { ...options, timeZone: 'UTC' })
}

export function outcome(run: Pick<InsightsRun, 'status' | 'reason'>): { label: string; tone: Tone } {
  const state = outcomeState(run.status)
  if (!state) return { label: run.status, tone: 'neutral' }
  return { label: run.status === 'abandoned' && run.reason === 'killed' ? 'Stopped' : stateLabel[state], tone: stateTone[state] }
}

export function agentLabel(harness: string, model: string, names: Record<string, string>): string {
  return `${names[harness] ?? harness} · ${model || 'default model'}`
}

export function sinceLabel(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })
}

const compact = new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 1 })

export function tokens(n: number): string {
  return `${compact.format(n)} ${n === 1 ? 'token' : 'tokens'}`
}

/** Rounded down, so 100% means every finished run or swarm succeeded. */
export function percent(succeeded: number, finished: number): number | null {
  return finished === 0 ? null : Math.floor((succeeded / finished) * 100)
}

export type Unit = 'runs' | 'swarms'

export const outcomeKeys = ['done', 'failed', 'abandoned'] as const
export type OutcomeKey = (typeof outcomeKeys)[number]

export interface Slice {
  id: string
  name: string
  color: string
  count: number
}

export interface Day {
  start: string
  counts: InsightsOutcome
  /** Members with at least one outcome that day, per outcome, in member order; empty for a personal chart. */
  members: Record<OutcomeKey, Slice[]>
}

/** One entry per day of the series: the unit's outcomes, and who they belong to when the result names members. */
export function chartDays(result: InsightsResult, unit: Unit): Day[] {
  const { series, members } = result.outcomes
  return series.map((bucket, i) => {
    const slices = (key: OutcomeKey): Slice[] =>
      members.flatMap((m) => {
        const count = m.series[i]?.[unit][key] ?? 0
        return count > 0 ? [{ id: m.member_id, name: m.display_name, color: m.color, count }] : []
      })
    return { start: bucket.start, counts: bucket[unit], members: { done: slices('done'), failed: slices('failed'), abandoned: slices('abandoned') } }
  })
}

export function chartMax(days: Day[]): number {
  return Math.max(1, ...days.flatMap((d) => outcomeKeys.map((key) => d.counts[key])))
}

export function dayWords(day: Day, unit: Unit): string {
  const counts = outcomeKeys.map((key) => {
    const who = day.members[key].map((m) => `${m.name} ${m.count}`).join(', ')
    return `${day.counts[key]} ${key}${who && ` (${who})`}`
  })
  return `${dayLabel(day.start, { weekday: 'short', month: 'short', day: 'numeric' })}: ${unit} ${counts.join(', ')}`
}

export type SectionLoad =
  | { kind: 'loading' }
  | { kind: 'ready'; result: InsightsResult }
  | { kind: 'failed'; message: string }
  | { kind: 'unsupported' }
