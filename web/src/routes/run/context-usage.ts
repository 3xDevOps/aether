import type { SessionUsage } from '@/lib/session-types'
import type { SessionItem } from '@/lib/session-types'
import type { Turn } from '@/store/session-rows'

export const cellCount = 216

/** The share of the window at which the meter turns to the needs-you tone. */
export const warnPercent = 90

export const usageFormat = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 })

export type SegmentKey = 'used' | 'earlier' | 'latest' | 'free'

export interface Segment {
  key: SegmentKey
  label: string
  tokens: number
  cells: number
}

export function percentUsed({ used, size }: SessionUsage): number {
  return Math.min(100, Math.round((used / size) * 100))
}

const report = (item: SessionItem) => (item.kind === 'usage' ? item.usage : undefined)

function lastReport(turn: Turn): SessionUsage | undefined {
  for (let i = turn.items.length - 1; i >= 0; i--) {
    const usage = report(turn.items[i]!)
    if (usage) return usage
  }
  return undefined
}

/**
 * The usage the agent reported when the turn before the newest one with a
 * report ended. Undefined when a report in the newest turn shrank or changed
 * the window, as a compaction does, since the split is then unknown.
 */
export function previousTurnUsage(turns: Turn[]): SessionUsage | undefined {
  let newest = turns.length - 1
  while (newest >= 0 && !lastReport(turns[newest]!)) newest--
  let baseline: SessionUsage | undefined
  for (let i = newest - 1; i >= 0 && !baseline; i--) baseline = lastReport(turns[i]!)
  if (!baseline) return undefined
  let seen = baseline
  for (const item of turns[newest]!.items) {
    const usage = report(item)
    if (!usage) continue
    if (usage.size !== seen.size || usage.used < seen.used) return undefined
    seen = usage
  }
  return baseline
}

/**
 * Splits the window for the grid and legend: used and free, with the used
 * part divided into earlier turns and the latest turn when `previous` is a
 * report of the same window that this one grew from.
 */
export function contextSegments(usage: SessionUsage, previous?: SessionUsage): Segment[] {
  const { used, size } = usage
  const usedCells = used > 0 ? Math.max(1, Math.round((cellCount * Math.min(used, size)) / size)) : 0
  const grown = previous && previous.size === size ? used - previous.used : 0
  const segments: Segment[] = []
  if (grown > 0) {
    const latestCells = Math.min(usedCells, Math.max(1, Math.round((cellCount * grown) / size)))
    segments.push(
      { key: 'earlier', label: 'Earlier context', tokens: used - grown, cells: usedCells - latestCells },
      { key: 'latest', label: 'Latest turn', tokens: grown, cells: latestCells },
    )
  } else {
    segments.push({ key: 'used', label: 'Used', tokens: used, cells: usedCells })
  }
  if (used < size) segments.push({ key: 'free', label: 'Free', tokens: size - used, cells: cellCount - usedCells })
  return segments
}

export function formatCost(cost: number, currency = 'USD'): string {
  try {
    return new Intl.NumberFormat(undefined, { style: 'currency', currency }).format(cost)
  } catch {
    return `${cost} ${currency}`
  }
}

const share = new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 })

/** A segment's part of the window, with a decimal only below 10%. */
export function formatShare(tokens: number, size: number): string {
  const percent = (tokens / size) * 100
  return share.format(percent < 10 ? percent : Math.round(percent))
}
