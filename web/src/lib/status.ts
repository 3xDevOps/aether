// Presentation states. UI-only: the wire run status enum is unchanged. A run
// shows exactly one state and one plain-words reason, derived worst-first and
// scoped to the viewer. See "Run state" in docs/dashboard-frontend.md.

import {
  isPaused,
  memberName,
  needsYou,
  supervised,
  waitingOn,
  type NeedsYouCondition,
  type StateContext,
} from '@/lib/needs-you'
import { isTerminal, type RunRecord } from '@/store/runs'

export type PresentationState = 'needs-you' | 'working' | 'paused' | 'done' | 'failed'

/** The three run groups the sidebar and the board list. */
export type RunGroup = 'needs-you' | 'working' | 'finished'

export interface RunPresentation {
  state: PresentationState
  reason: string
  /** The condition that needs the viewer, when the state is needs-you. */
  needsYou?: NeedsYouCondition
}

function workingReason(run: RunRecord, ctx: StateContext): string {
  if (run.status === 'queued') return 'Queued'
  if (run.status === 'provisioning') return 'Starting'
  const waiter = waitingOn(run, ctx)
  if (waiter !== undefined) return `Waiting for ${memberName(waiter, ctx)}`
  if (supervised(run, ctx) && run.status === 'needs-attention') return 'Waiting for the integrator'
  if (run.activity) return `${run.activity.verb} ${run.activity.target}`.trimEnd()
  return run.status === 'needs-attention' ? 'Agent idle' : 'Agent working'
}

function finishedReason(run: RunRecord): string {
  switch (run.status) {
    case 'merged':
      return 'Merged'
    case 'abandoned':
      return 'Closed without merging'
    case 'completed':
      return 'Finished'
    case 'interrupted':
      return run.reason ? `Interrupted: ${run.reason}` : 'Interrupted'
    default:
      return run.reason ? `Failed: ${run.reason}` : 'Failed'
  }
}

/** A run's one state and reason, as this viewer sees it. */
export function presentRun(run: RunRecord, ctx: StateContext): RunPresentation {
  const condition = needsYou(run, ctx)
  if (condition) return { state: 'needs-you', reason: condition.reason(run, ctx), needsYou: condition }
  if (isTerminal(run.status)) {
    return {
      state: run.status === 'failed' || run.status === 'interrupted' ? 'failed' : 'done',
      reason: finishedReason(run),
    }
  }
  if (isPaused(run, ctx)) return { state: 'paused', reason: 'Paused' }
  return { state: 'working', reason: workingReason(run, ctx) }
}

export function groupOf(state: PresentationState): RunGroup {
  switch (state) {
    case 'needs-you':
      return 'needs-you'
    case 'working':
    case 'paused':
      return 'working'
    default:
      return 'finished'
  }
}

const fallbackLabelLength = 120

/** A run's human title. An untitled run is named by its bounded first prompt line. */
export function runLabel(run: { task: string; title?: string }): string {
  const title = run.title?.trim()
  if (title) return title
  const line = run.task.split('\n').find((l) => l.trim())?.trim() ?? ''
  if (!line) return 'Untitled run'
  const chars = Array.from(line)
  if (chars.length <= fallbackLabelLength) return line
  const cut = chars.slice(0, fallbackLabelLength).join('')
  const space = cut.search(/\s\S*$/)
  return `${(space > fallbackLabelLength / 2 ? cut.slice(0, space) : cut).trimEnd()}…`
}

export const stateLabel: Record<PresentationState, string> = {
  'needs-you': 'Needs you',
  working: 'Working',
  paused: 'Paused',
  done: 'Done',
  failed: 'Failed',
}

export const groupLabel: Record<RunGroup, string> = {
  'needs-you': 'Needs you',
  working: 'Working',
  finished: 'Finished',
}

/** The chip colour each state reads in. */
export const stateTone: Record<PresentationState, 'warning' | 'accent' | 'default' | 'success' | 'danger'> = {
  'needs-you': 'warning',
  working: 'accent',
  paused: 'default',
  done: 'success',
  failed: 'danger',
}

// Token classes only - no colour literals in components.
export const stateDotClass: Record<PresentationState, string> = {
  'needs-you': 'bg-state-needs-you',
  working: 'bg-state-working',
  paused: 'bg-state-paused',
  done: 'bg-state-done',
  failed: 'bg-state-failed',
}
