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

/**
 * "deleted today" / "deleted in 1 day" / "deleted in N days" for an archived
 * run's `deletes_at`, computed fresh each render rather than off a stored
 * window. Whole hours floor into days, so this undercounts rather than
 * overstates the time left on a destructive countdown: under 24h - a date
 * already past included, a sweep due any moment - reads "today", 24h up to
 * 48h reads "in 1 day". An unparseable value returns "" so the caller can
 * render no badge at all.
 */
export function deletesInLabel(iso: string, now = Date.now()): string {
  const deletesAt = new Date(iso).getTime()
  if (!Number.isFinite(deletesAt)) return ''
  const hours = Math.floor((deletesAt - now) / 3_600_000)
  if (hours < 24) return 'deleted today'
  const days = Math.floor(hours / 24)
  if (days === 1) return 'deleted in 1 day'
  return `deleted in ${days} days`
}

/**
 * A version with its release-tag prefix off. Release tags are "v1.2.3", the
 * desktop shell records "1.2.3", and the two have to compare equal.
 */
export function bareVersion(version: string): string {
  return version.replace(/^v/, '')
}

/** The host of an edge URL, as the client's own errors name it. */
export function edgeHost(edge: string): string {
  return new URL(edge).host
}

/**
 * What a local link points at: its SSH address, or for a link through an
 * edge with none, the server id and the edge.
 */
export function linkTarget(link: Pick<LinkStatus, 'addr' | 'server_id' | 'edge_url'>): string {
  if (link.addr || !link.server_id || !link.edge_url) return link.addr
  return `${link.server_id} through ${edgeHost(link.edge_url)}`
}

/** An error's text, whatever the throw site handed us. */
/** The name people know an edge sign-in provider by. */
export const providerName: Record<string, string> = { github: 'GitHub' }

export function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

export const money = new Intl.NumberFormat(undefined, {
  style: 'currency',
  currency: 'USD',
})

/** What a budget state is called wherever it is shown. */
export const budgetStateLabel: Record<BudgetState, string> = {
  ok: 'within budget',
  warn: 'nearing the cap',
  exceeded: 'past the cap',
}

/** Display names for the harnesses whose CLI is not called what Aether
 * calls it. Anything absent is shown by its registry name. */
export const friendly: Record<string, string> = {
  claude: 'Claude Code',
  codex: 'Codex',
  omp: 'oh-my-pi',
}
