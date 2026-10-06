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

export function timeAgo(iso: string, now = Date.now()): string {
  let delta = (new Date(iso).getTime() - now) / 1000
  if (!Number.isFinite(delta)) return ''
  for (const [unit, span] of steps) {
    if (Math.abs(delta) < span) return relative.format(Math.round(delta), unit)
    delta /= span
  }
  return ''
}

/** Floors to whole days so a destructive countdown never overstates the time left. */
export function deletesInLabel(iso: string, now = Date.now()): string {
  const deletesAt = new Date(iso).getTime()
  if (!Number.isFinite(deletesAt)) return ''
  const hours = Math.floor((deletesAt - now) / 3_600_000)
  if (hours < 24) return 'deleted today'
  const days = Math.floor(hours / 24)
  if (days === 1) return 'deleted in 1 day'
  return `deleted in ${days} days`
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

