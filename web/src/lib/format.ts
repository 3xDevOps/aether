import type { BudgetState, LinkStatus } from '@/lib/types'

const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']

export function formatBytes(bytes: number): string {
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value < 10 && unit > 0 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`
}

const relative = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' })
const steps: [Intl.RelativeTimeFormatUnit, number][] = [
  ['second', 60],
  ['minute', 60],
  ['hour', 24],
  ['day', 7],
  ['week', 4.35],
  ['month', 12],
  ['year', Number.POSITIVE_INFINITY],
]

function relativeStep(iso: string, now: number): [number, Intl.RelativeTimeFormatUnit] | undefined {
  let delta = (new Date(iso).getTime() - now) / 1000
  if (!Number.isFinite(delta)) return undefined
  for (const [unit, span] of steps) {
    const value = Math.round(delta)
    // A value that rounds up to the span belongs to the next unit: 1 hour, not 60 minutes.
    if (Math.abs(delta) < span && Math.abs(value) < span) return [value, unit]
    delta /= span
  }
  return undefined
}

export function timeAgo(iso: string, now = Date.now()): string {
  const step = relativeStep(iso, now)
  return step ? relative.format(...step) : ''
}

const compactUnits: Partial<Record<Intl.RelativeTimeFormatUnit, string>> = {
  minute: 'm',
  hour: 'h',
  day: 'd',
  week: 'w',
  month: 'mo',
  year: 'y',
}

/** `timeAgo` in a few characters: "now" under a minute, then "15m", "3h", "2d". */
export function compactAge(iso: string, now = Date.now()): string {
  const step = relativeStep(iso, now)
  if (!step) return ''
  const [value, unit] = step
  const suffix = compactUnits[unit]
  return suffix && value < 0 ? `${-value}${suffix}` : 'now'
}

/** Release tags are "v1.2.3" but the desktop shell records "1.2.3". */
export function bareVersion(version: string): string {
  return version.replace(/^v/, '')
}

export function edgeHost(edge: string): string {
  return new URL(edge).host
}

/** The SSH address, or for an edge link without one, the server id and edge. */
export function linkTarget(link: Pick<LinkStatus, 'addr' | 'server_id' | 'edge_url'>): string {
  if (link.addr || !link.server_id || !link.edge_url) return link.addr
  return `${link.server_id} through ${edgeHost(link.edge_url)}`
}

export const providerName: Record<string, string> = { github: 'GitHub' }

export function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

const sourcePrefix = /^[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)*: (?=\S)/

export function errorSentence(err: unknown): string {
  let text = message(err)
  for (let next = text.replace(sourcePrefix, ''); next !== text; next = next.replace(sourcePrefix, '')) text = next
  return text
}

export const money = new Intl.NumberFormat(undefined, {
  style: 'currency',
  currency: 'USD',
})

export const budgetStateLabel: Record<BudgetState, string> = {
  ok: 'within budget',
  warn: 'nearing the cap',
  exceeded: 'past the cap',
}

