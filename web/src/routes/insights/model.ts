import type { Tone } from '@/components/ui/status-dot'
import { outcomeState, stateLabel, stateTone } from '@/lib/status'
import type { InsightsBucket, InsightsResult, InsightsRun, InsightsTotals } from '@/lib/types'

export type Period = '7d' | '30d' | '90d' | '12m'

export const periods: { value: Period; label: string; previous: string }[] = [
  { value: '7d', label: 'Last 7 days', previous: 'the 7 days before' },
  { value: '30d', label: 'Last 30 days', previous: 'the 30 days before' },
  { value: '90d', label: 'Last 90 days', previous: 'the 90 days before' },
  { value: '12m', label: 'Last 12 months', previous: 'the 12 months before' },
]

const days: Record<Exclude<Period, '12m'>, number> = { '7d': 7, '30d': 30, '90d': 90 }

/** The period ends at the next local midnight, so today counts. Twelve months
 * start on a Monday, where the server's weekly buckets start. */
export function periodRange(period: Period, now: Date): { from: Date; to: Date } {
  const to = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1)
  if (period !== '12m') return { from: new Date(to.getFullYear(), to.getMonth(), to.getDate() - days[period]), to }
  const year = new Date(to.getFullYear() - 1, to.getMonth(), to.getDate())
  return { from: new Date(year.getFullYear(), year.getMonth(), year.getDate() - ((year.getDay() + 6) % 7)), to }
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
  return { finished: totals.finished, ended, percent: ended === 0 ? null : Math.floor((totals.finished / ended) * 100) }
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

export function addDays(date: string, n: number): string {
  const day = calendarDay(date)
  day.setUTCDate(day.getUTCDate() + n)
  return day.toISOString().slice(0, 10)
}

export type BucketRuns = { kind: 'loading' } | { kind: 'ready'; runs: InsightsRun[] } | { kind: 'failed'; message: string }

export interface Selection {
  start: string
  end: string
  label: string
}

export function bucketSelection(start: string, bucket: InsightsResult['bucket']): Selection {
  if (bucket === 'day') return { start, end: addDays(start, 1), label: dayLabel(start, { weekday: 'short', month: 'short', day: 'numeric' }) }
  return { start, end: addDays(start, 7), label: `the week of ${dayLabel(start)}` }
}

/** A selection's instants, held inside the period: a phone's weekly bars can start before it. */
export function bucketBounds(selection: Selection, result: Pick<InsightsResult, 'from' | 'to' | 'time_zone'>): { from: Date; to: Date } {
  const utc = result.time_zone === '' || result.time_zone === 'UTC'
  const midnight = (date: string) => {
    const [y, m, d] = date.split('-').map(Number)
    return utc ? new Date(Date.UTC(y, m - 1, d)) : new Date(y, m - 1, d)
  }
  return {
    from: new Date(Math.max(midnight(selection.start).getTime(), Date.parse(result.from))),
    to: new Date(Math.min(midnight(selection.end).getTime(), Date.parse(result.to))),
  }
}

/** On a phone, more than 30 daily bars become weekly ones so each stays a usable target. */
export function chartSeries(result: InsightsResult, mobile: boolean): { series: InsightsBucket[]; bucket: InsightsResult['bucket'] } {
  if (!mobile || result.bucket !== 'day' || result.series.length <= 30) return { series: result.series, bucket: result.bucket }
  const weeks = new Map<string, InsightsBucket>()
  for (const day of result.series) {
    const start = addDays(day.start, -((calendarDay(day.start).getUTCDay() + 6) % 7))
    const week = weeks.get(start) ?? { start, launched: 0, completed: 0, merged: 0, failed: 0, interrupted: 0, abandoned: 0 }
    weeks.set(start, {
      start,
      launched: week.launched + day.launched,
      completed: week.completed + day.completed,
      merged: week.merged + day.merged,
      failed: week.failed + day.failed,
      interrupted: week.interrupted + day.interrupted,
      abandoned: week.abandoned + day.abandoned,
    })
  }
  return { series: [...weeks.values()], bucket: 'week' }
}

export function outcome(run: Pick<InsightsRun, 'status' | 'reason'>): { label: string; tone: Tone } {
  const state = outcomeState(run.status)
  if (!state) return { label: run.status, tone: 'neutral' }
  return { label: run.status === 'abandoned' && run.reason === 'killed' ? 'Stopped' : stateLabel[state], tone: stateTone[state] }
}

export function agentLabel(harness: string, model: string, names: Record<string, string>): string {
  return `${names[harness] ?? harness} · ${model || 'default model'}`
}

export function headline(result: InsightsResult, period: string, oneWorkspace: boolean): string {
  const parts = [`${plural(result.totals.finished, 'run')} finished`]
  if (result.swarms.completed > 0) parts.push(`${plural(result.swarms.completed, 'swarm')} completed`)
  const workspaces = result.by_workspace.filter((w) => w.launched > 0 || w.finished > 0).length
  const across = !oneWorkspace && workspaces > 1 ? ` across ${plural(workspaces, 'workspace')}` : ''
  return `${period}: ${parts.join(' and ')}${across}.`
}

export function isEmpty(result: InsightsResult): boolean {
  const t = result.totals
  return t.launched + t.finished + t.failed + t.interrupted + t.abandoned + t.active === 0 && result.swarms.launched === 0 && result.recent.length === 0
}

export function sinceLabel(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })
}
