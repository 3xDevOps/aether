import type { Tone } from '@/components/ui/status-dot'
import type { InsightsResult, InsightsRun, InsightsTotals } from '@/lib/types'

export type Period = '7d' | '30d' | '90d' | '12m'

export const periods: { value: Period; label: string; previous: string }[] = [
  { value: '7d', label: 'Last 7 days', previous: 'the 7 days before' },
  { value: '30d', label: 'Last 30 days', previous: 'the 30 days before' },
  { value: '90d', label: 'Last 90 days', previous: 'the 90 days before' },
  { value: '12m', label: 'Last 12 months', previous: 'the 12 months before' },
]

const days: Record<Exclude<Period, '12m'>, number> = { '7d': 7, '30d': 30, '90d': 90 }

/** The period ends at the next local midnight, so today counts. */
export function periodRange(period: Period, now: Date): { from: Date; to: Date } {
  const to = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1)
  const from = period === '12m'
    ? new Date(to.getFullYear() - 1, to.getMonth(), to.getDate())
    : new Date(to.getFullYear(), to.getMonth(), to.getDate() - days[period])
  return { from, to }
}

export function rfc3339(date: Date): string {
  return date.toISOString().replace(/\.\d{3}Z$/, 'Z')
}

export function plural(n: number, one: string, many = `${one}s`): string {
  return `${n.toLocaleString()} ${n === 1 ? one : many}`
}

export function versus(current: number, previous: number, before: string, format: (n: number) => string = (n) => n.toLocaleString()): string {
  if (current === previous) return `Same as ${before}`
  return `${current > previous ? 'Up' : 'Down'} from ${format(previous)} ${before}`
}

export interface Rate {
  finished: number
  ended: number
  percent: number | null
}

export function successRate(totals: InsightsTotals): Rate {
  const ended = totals.finished + totals.failed + totals.interrupted + totals.abandoned
  return { finished: totals.finished, ended, percent: ended === 0 ? null : Math.round((totals.finished / ended) * 100) }
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

/** The YYYY-MM-DD the instant falls on in the zone. */
export function localDate(iso: string, timeZone: string): string {
  return new Intl.DateTimeFormat('en-CA', { timeZone: timeZone || 'UTC', year: 'numeric', month: '2-digit', day: '2-digit' }).format(new Date(iso))
}

/** Dates on the wire are local calendar days; reading them as UTC keeps the day. */
export function calendarDay(date: string): Date {
  return new Date(`${date}T00:00:00Z`)
}

export function dayLabel(date: string, options: Intl.DateTimeFormatOptions = { month: 'short', day: 'numeric' }): string {
  return calendarDay(date).toLocaleDateString(undefined, { ...options, timeZone: 'UTC' })
}

export function addDays(date: string, n: number): string {
  const day = calendarDay(date)
  day.setUTCDate(day.getUTCDate() + n)
  return day.toISOString().slice(0, 10)
}

export interface Selection {
  start: string
  end: string
  label: string
}

export function bucketSelection(start: string, bucket: InsightsResult['bucket']): Selection {
  if (bucket === 'day') return { start, end: addDays(start, 1), label: dayLabel(start, { weekday: 'short', month: 'short', day: 'numeric' }) }
  return { start, end: addDays(start, 7), label: `the week of ${dayLabel(start)}` }
}

export function finishedIn(run: InsightsRun, selection: Selection, timeZone: string): boolean {
  const day = localDate(run.finished_at, timeZone)
  return day >= selection.start && day < selection.end
}

export function outcome(run: Pick<InsightsRun, 'status' | 'reason'>): { label: string; tone: Tone } {
  switch (run.status) {
    case 'completed':
      return { label: 'Completed', tone: 'done' }
    case 'merged':
      return { label: 'Merged', tone: 'done' }
    case 'failed':
      return { label: 'Failed', tone: 'failed' }
    case 'interrupted':
      return { label: 'Interrupted', tone: 'failed' }
    case 'abandoned':
      return { label: run.reason === 'killed' ? 'Killed' : 'Abandoned', tone: 'paused' }
    default:
      return { label: run.status, tone: 'neutral' }
  }
}

export function agentLabel(harness: string, model: string, names: Record<string, string>): string {
  return `${names[harness] ?? harness} · ${model || 'default model'}`
}

export function headline(result: InsightsResult, period: string, oneWorkspace: boolean): string {
  const parts = [`${plural(result.totals.finished, 'run')} finished`]
  if (result.swarms.completed > 0) parts.push(`${plural(result.swarms.completed, 'swarm')} completed`)
  const repositories = result.by_workspace.filter((w) => w.launched > 0 || w.finished > 0).length
  const across = !oneWorkspace && repositories > 1 ? ` across ${plural(repositories, 'repository', 'repositories')}` : ''
  return `${period}: ${parts.join(' and ')}${across}.`
}

export function isEmpty(result: InsightsResult): boolean {
  const t = result.totals
  return t.launched + t.finished + t.failed + t.interrupted + t.abandoned + t.active === 0 && result.swarms.launched === 0 && result.recent.length === 0
}

export function sinceLabel(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })
}
