import type { Tone } from '@/components/ui/status-dot'
import type { WaitCheckCounts, WaitChecks, WaitCondition, WaitState } from '@/lib/types'

export const waitStateLabel: Record<WaitState, string> = {
  pending: 'Pending',
  resolved: 'Resolved',
  cancelled: 'Cancelled',
  superseded: 'Replaced',
  expired: 'Expired',
  error: 'Unreadable',
}

const goodOutcomes = new Set(['merged', 'passed', 'completed', 'reported-success'])

export function waitTone(w: WaitCondition): Tone {
  switch (w.state) {
    case 'pending':
      return 'waiting'
    case 'resolved':
      return w.outcome && goodOutcomes.has(w.outcome) ? 'done' : 'failed'
    case 'error':
      return 'failed'
    default:
      return 'closed'
  }
}

/** The word after the target: "o/r#7 merged", "o/r#7 checks", "<run> finished". */
export const waitVerb: Record<WaitCondition['kind'], string> = {
  pr_merged: 'merged',
  pr_checks: 'checks final',
  run_finished: 'finished',
}

export function prURL(w: WaitCondition): string | undefined {
  return w.repo && w.number ? `https://github.com/${w.repo}/pull/${w.number}` : undefined
}

export function countsText(c: WaitCheckCounts): string {
  const parts = [`${c.passed} passed`, `${c.failed} failed`, `${c.pending} running`]
  if (c.cancelled > 0) parts.push(`${c.cancelled} cancelled`)
  return parts.join(', ')
}

export function noChecks(c: WaitChecks): boolean {
  return c.passed + c.failed + c.pending + c.cancelled === 0 && !c.missing?.length
}

/** Waits the server still checks come first, then the newest. */
export function orderWaits(waits: WaitCondition[]): WaitCondition[] {
  return [...waits].sort((a, b) =>
    Number(b.state === 'pending') - Number(a.state === 'pending') || b.revision - a.revision || b.created_at.localeCompare(a.created_at))
}

/** A pending check that is a minute past due is not being verified right now. */
export function checkOverdue(w: WaitCondition, now: number): boolean {
  return w.state === 'pending' && w.next_check_at !== undefined && Date.parse(w.next_check_at) + 60_000 < now
}

export type Delivery = { tone: Tone; text: string }

export function deliveryOf(w: WaitCondition): Delivery | undefined {
  if (w.delivered_at) return { tone: 'done', text: 'Delivered to the agent' }
  if (w.state !== 'resolved' && w.state !== 'expired' && w.state !== 'error') return undefined
  const attempts = w.delivery_attempts
  if (w.last_delivery_error) return { tone: 'failed', text: `Not delivered after ${attempts} ${attempts === 1 ? 'attempt' : 'attempts'}: ${w.last_delivery_error}` }
  return { tone: 'waiting', text: attempts > 0 ? `Delivering, ${attempts} ${attempts === 1 ? 'attempt' : 'attempts'} so far` : 'Waiting to be delivered' }
}
