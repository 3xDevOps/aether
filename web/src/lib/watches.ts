import type { Tone } from '@/components/ui/status-dot'
import { routeFromQuery } from '@/lib/url-state'
import type { Route } from '@/store/ui'
import type { Watch, WatchCondition, WatchDelivery, WatchState, WatchTarget, WatchTargetKind } from '@/lib/types'

export interface ConditionChoice {
  value: WatchCondition
  label: string
  description: string
  /** The server watches for it when no condition is named. */
  default: boolean
}

// Each claim stays its own: an agent's report is not a merge, a merge is not a delivery.
export const conditionChoices: Record<WatchTargetKind, ConditionChoice[]> = {
  run: [
    { value: 'needs_you', label: 'Needs you', description: 'The agent asks a question, wants a permission or is blocked. Once per need.', default: true },
    { value: 'finished', label: 'Finished', description: 'The run ends: the agent reported done or failed, it was stopped, or a member closed it. Finished means the agent reported done, not that anything merged.', default: true },
    { value: 'pr_merged', label: 'Pull request merged', description: "The run's pull request merged on GitHub. Merged means GitHub merged the PR.", default: false },
    { value: 'ci', label: 'CI finished', description: "The checks on the run's pull request finish, pass or fail.", default: false },
  ],
  mission: [
    { value: 'needs_you', label: 'Needs you', description: 'The integrator asks you something, is blocked or stops. Never for each worker.', default: true },
    { value: 'finished', label: 'Finished', description: 'The swarm completes or is cancelled.', default: true },
    { value: 'delivered', label: 'Delivered', description: 'The integrated result lands on its target branch, or is proposed as a pull request.', default: false },
  ],
  pr: [
    { value: 'ci', label: 'CI finished', description: 'Every check on the newest commit finishes, pass or fail. Again after a rerun.', default: true },
    { value: 'review', label: 'Reviews', description: 'Someone approves, requests changes or comments.', default: true },
    { value: 'merged', label: 'Merged', description: 'GitHub merged the pull request.', default: true },
    { value: 'closed', label: 'Closed', description: 'The pull request was closed without merging.', default: false },
  ],
}

export function defaultConditions(kind: WatchTargetKind): WatchCondition[] {
  return conditionChoices[kind].filter((choice) => choice.default).map((choice) => choice.value)
}

export function conditionLabel(kind: WatchTargetKind, condition: WatchCondition): string {
  return conditionChoices[kind].find((choice) => choice.value === condition)?.label ?? condition
}

export const stateWords: Record<WatchState, { tone: Tone; label: string }> = {
  active: { tone: 'working', label: 'Watching' },
  paused: { tone: 'paused', label: 'Paused' },
  error: { tone: 'failed', label: 'Cannot check' },
  done: { tone: 'done', label: 'Done' },
  expired: { tone: 'ended', label: 'Expired' },
}

/** Active and paused watches, and those whose checks fail but keep retrying. */
export function isLive(watch: Pick<Watch, 'state'>): boolean {
  return watch.state === 'active' || watch.state === 'paused' || watch.state === 'error'
}

export function targetKindLabel(target: WatchTarget): string {
  switch (target.kind) {
    case 'run':
      return 'Run'
    case 'mission':
      return 'Swarm'
    case 'pr':
      return `PR #${target.number ?? '?'}${target.repo ? ` · ${target.repo}` : ''}`
  }
}

/** One line per channel, saying what reached the member and what did not. */
export function deliveryText(delivery: WatchDelivery): string {
  const detail = delivery.detail?.trim()
  switch (delivery.channel) {
    case 'push':
      switch (delivery.status) {
        case 'delivered':
          return detail ? `Push delivered to ${detail}` : 'Push delivered'
        case 'disabled':
          return detail ? `Push off: ${detail}` : 'Push off'
        case 'no_destination':
          return 'No push device - in-app only'
        case 'held_quiet_hours':
          return detail ? `Held for quiet hours: ${detail}` : 'Held for quiet hours'
        case 'failed':
          return `Push failed: ${detail || 'no reason given'}`
        case 'cleared':
          return 'Push not sent: resolved before delivery'
        case 'pending':
          return detail ? `Push pending: ${detail}` : 'Push pending'
      }
      break
    case 'in_app':
      return delivery.status === 'failed' ? `In-app failed: ${detail || 'no reason given'}` : 'In-app'
  }
  return `${delivery.channel}: ${delivery.status}${detail ? ` (${detail})` : ''}`
}

/** What a live watch does now, from its own conditions and state. */
export function watchHint(watch: Pick<Watch, 'target' | 'conditions' | 'state' | 'error'>): string {
  if (watch.state === 'paused') return 'Watch paused: no notices until you resume it.'
  if (watch.state === 'error') return `The server cannot check this watch: ${watch.error || 'no reason given'}. It keeps retrying.`
  const conditions = watch.conditions.map((c) => conditionLabel(watch.target.kind, c)).join(', ')
  return `Notifies you on: ${conditions}. Notices appear in the dashboard, and on your push devices when push is on.`
}

export function deliveryTone(delivery: WatchDelivery): Tone {
  switch (delivery.status) {
    case 'delivered':
      return 'done'
    case 'failed':
      return 'failed'
    case 'held_quiet_hours':
    case 'pending':
      return 'paused'
    default:
      return 'neutral'
  }
}

/** The repository and number a GitHub pull request URL names. */
export function pullRequestTarget(url: string, runID?: string): WatchTarget | null {
  const match = /^https:\/\/github\.com\/([^/]+\/[^/]+)\/pull\/(\d+)/.exec(url)
  if (!match) return null
  return { kind: 'pr', repo: match[1], number: Number(match[2]), ...(runID ? { run_id: runID } : {}) }
}

export function watchRoute(watchID: string): { name: string; params: Record<string, string> } {
  return { name: 'settings', params: { section: 'watches', watch: watchID } }
}

/** The days a new watch may run for, 14 by default, 90 at most. */
export const defaultExpiryDays = 14
export const maxExpiryDays = 90

/** A dashboard link opens in place; anything else, such as a pull request on GitHub, is a page of its own. */
export function linkTarget(link: string): { route: Route } | { href: string } {
  if (link.startsWith('/?') || link.startsWith('?')) return { route: routeFromQuery(new URLSearchParams(link.slice(link.indexOf('?')))) }
  return { href: link }
}
