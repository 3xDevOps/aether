import type { Tone } from '@/components/ui/status-dot'
import {
  awaitsCompletion,
  isPaused,
  memberName,
  needsYou,
  unreadMail,
  unreadReason,
  waitingOn,
  type NeedsYouCondition,
  type StateContext,
} from '@/lib/needs-you'
import { timeAgo } from '@/lib/format'
import { plainReason } from '@/lib/reasons'
import type { BackgroundTask } from '@/lib/session-types'
import type { RunActivity, RunStatus } from '@/lib/types'
import { isTerminal, type RunRecord } from '@/store/runs'

/** A live run's state; one without a state is shown by its status. */
type LiveState = Exclude<RunActivity, 'idle' | 'finished'> | 'ready' | 'running'

type Outcome = 'completed' | 'merged' | 'closed' | 'interrupted'

/** A live run's state, a finished run's outcome, or Paused. */
export type PresentationState = LiveState | Outcome | 'paused'

export type RunGroup = 'needs-you' | 'working' | 'review' | 'finished'

export interface RunPresentation {
  state: PresentationState
  reason: string
  /** What happens next, or what the viewer can do; absent when nothing is to say. */
  next?: string
  needsYou?: NeedsYouCondition
  unread?: number
}

export function backgroundReason(tasks: BackgroundTask[]): string {
  const held = tasks.filter((task) => task.holds).length
  return held === 0 ? 'Working in background' : `Working in background: ${held} ${held === 1 ? 'task' : 'tasks'}`
}

const liveActivities = new Set<string>(['working', 'waiting-agents', 'waiting-ci', 'waiting-tools', 'waiting', 'answered', 'needs-you', 'failed', 'disconnected'])

/**
 * A live run's state from its wire status and activity. A reported success reads Ready for review.
 * A parked run with no known state, or the `idle` older servers wrote, ended its turn without a
 * report; a running one this dashboard cannot read is shown as Running.
 */
export function liveStateOf(status: 'running' | 'needs-attention', state: string | undefined): LiveState {
  if (state === 'finished') return 'ready'
  if (state === 'idle') return 'waiting'
  if (state && liveActivities.has(state)) return state as LiveState
  return status === 'needs-attention' ? 'waiting' : 'running'
}

const stateReasons: Record<LiveState, string> = {
  working: 'Agent working',
  'waiting-agents': 'Waiting for its agents to report',
  'waiting-ci': 'Waiting for CI',
  'waiting-tools': 'Waiting for a background process',
  waiting: 'Turn ended without a report',
  answered: 'Agent answered',
  'needs-you': 'The agent needs you',
  ready: 'Agent reported success',
  failed: 'Agent reported failure',
  disconnected: 'Agent disconnected',
  running: 'Running',
}

const nudge = 'The agent checks back on its next turn; send a message to nudge it.'

const stateNext: Partial<Record<PresentationState, string>> = {
  'waiting-agents': 'Mail from its agents can wake it; send a message to nudge it.',
  'waiting-ci': nudge,
  'waiting-tools': nudge,
  waiting: 'Send a message to continue.',
  ready: 'Mark it complete, or send a message to keep going.',
}

function liveState(run: RunRecord, ctx: StateContext): Omit<RunPresentation, 'needsYou' | 'unread'> {
  if (run.status === 'queued') return { state: 'working', reason: 'Queued' }
  if (run.status === 'provisioning') return { state: 'working', reason: 'Starting' }
  const state = liveStateOf(run.status as 'running' | 'needs-attention', run.state)
  const waiter = waitingOn(run, ctx)
  if (waiter !== undefined) return { state: state === 'needs-you' ? 'waiting' : state, reason: `Waiting for ${memberName(waiter, ctx)}` }
  if (state === 'needs-you' && run.mission_role === 'worker') {
    return { state: 'waiting-agents', reason: 'Waiting for the integrator', next: 'It continues when the integrator answers.' }
  }
  if (state === 'working' && run.toolActivity) {
    return { state, reason: `${run.toolActivity.verb} ${run.toolActivity.target}`.trimEnd() }
  }
  const reason = run.reason && run.reason !== 'agent resumed' ? plainReason(run.reason) : stateReasons[state]
  if (state === 'failed') return { state, reason: `${reason}. Run remains open for follow-up.` }
  // Only Claude Code reports held background tasks, and it starts a turn when one ends.
  if (run.reason?.startsWith('working in background: ')) return { state, reason, next: 'Claude Code resumes it when a background task ends.' }
  return { state, reason, next: stateNext[state] }
}

const outcomes: Partial<Record<RunStatus, PresentationState>> = {
  completed: 'completed',
  merged: 'merged',
  abandoned: 'closed',
  failed: 'failed',
  interrupted: 'interrupted',
}

/** A finished run's outcome; undefined while the run is live. */
export function outcomeState(status: RunStatus): PresentationState | undefined {
  return outcomes[status]
}

function finishedReason(run: RunRecord): string {
  switch (run.status) {
    case 'merged':
      return 'Merged'
    case 'abandoned':
      return 'Closed without merging'
    case 'completed':
      return run.review === 'accepted' ? 'Marked complete' : 'Finished'
    case 'interrupted':
      return run.reason ? `Interrupted: ${plainReason(run.reason)}` : 'Interrupted'
    default:
      return run.reason ? `Failed: ${plainReason(run.reason)}` : 'Failed'
  }
}

export function presentRun(run: RunRecord, ctx: StateContext): RunPresentation {
  const condition = needsYou(run, ctx)
  if (condition) {
    return { state: condition.state, reason: condition.reason(run, ctx), next: stateNext[condition.state], needsYou: condition }
  }
  if (awaitsCompletion(run) && !isPaused(run, ctx)) {
    const reason = run.reason && !isTerminal(run.status) ? plainReason(run.reason) : stateReasons.ready
    return { state: 'ready', reason, next: stateNext.ready }
  }
  if (isTerminal(run.status)) {
    return { state: outcomeState(run.status)!, reason: finishedReason(run) }
  }
  if (isPaused(run, ctx)) return { state: 'paused', reason: 'Paused' }
  const live = liveState(run, ctx)
  const unread = unreadMail(run, ctx.now)
  if (unread > 0 && waitingOn(run, ctx) === undefined) {
    return { state: live.state, reason: unreadReason(unread, run.oldest_unacked_at!, ctx.now), unread }
  }
  return live
}

/** The reason worth printing beside the state's label, without the words that restate it. */
export function stateDetail({ state, reason }: Pick<RunPresentation, 'state' | 'reason'>): string | undefined {
  const label = stateLabel[state]
  if (reason === label || reason === stateReasons.working) return undefined
  return reason.startsWith(`${label}: `) ? reason.slice(label.length + 2) : reason
}

/**
 * The tooltip every surface gives a run's state: the state, since when, the reason the server
 * gave, and what happens next.
 */
export function stateTitle({ state, reason, next }: Pick<RunPresentation, 'state' | 'reason' | 'next'>, since: string, now = Date.now()): string {
  const ago = timeAgo(since, now)
  const head = ago ? `${stateLabel[state]} since ${ago}` : stateLabel[state]
  const said = reason && reason !== stateLabel[state] ? ` — ${reason.replace(/\.$/, '')}.` : '.'
  return next ? `${head}${said} ${next}` : `${head}${said}`
}

/** A row lists in Needs you when a condition applies to the viewer, whatever its label. */
export function groupOf({ state, needsYou }: Pick<RunPresentation, 'state' | 'needsYou'>): RunGroup {
  if (needsYou) return 'needs-you'
  if (state === 'ready') return 'review'
  return finishedStates.has(state) ? 'finished' : 'working'
}

const finishedStates = new Set<PresentationState>(['completed', 'merged', 'closed', 'interrupted', 'failed'])

/** A run that ended without its work done, which counts and sorts as failed. */
export function endedBadly(state: PresentationState): boolean {
  return state === 'failed' || state === 'interrupted'
}

const fallbackLabelLength = 120

/**
 * Holds a label to the fallback's length, cut at a word when one is near. One already cut there, as
 * the server's provisional title is (internal/scheduler/runmeta_title.go), is returned as it is.
 */
export function clipLabel(line: string): string {
  const chars = Array.from(line)
  if (chars.length <= fallbackLabelLength || (chars.length === fallbackLabelLength + 1 && line.endsWith('…'))) return line
  const cut = chars.slice(0, fallbackLabelLength).join('')
  const space = cut.search(/\s\S*$/)
  return `${(space > fallbackLabelLength / 2 ? cut.slice(0, space) : cut).trimEnd()}…`
}

/** A child run's purpose is the label its parent chose for it. */
export function runLabel(run: { task: string; title?: string; child_purpose?: string }): string {
  const title = run.child_purpose?.trim() || run.title?.trim()
  if (title) return title
  const line = run.task.split('\n').find((l) => l.trim())?.trim() ?? ''
  return line ? clipLabel(line) : 'Untitled run'
}

export const stateLabel: Record<PresentationState, string> = {
  working: 'Working',
  'waiting-agents': 'Waiting for agents',
  'waiting-ci': 'Waiting for CI',
  'waiting-tools': 'Waiting for process',
  waiting: 'Waiting',
  answered: 'Answered',
  'needs-you': 'Needs you',
  ready: 'Ready for review',
  failed: 'Failed',
  disconnected: 'Disconnected',
  completed: 'Completed',
  merged: 'Merged',
  closed: 'Closed',
  interrupted: 'Interrupted',
  paused: 'Paused',
  running: 'Running',
}

/** Each state's dot: the tone picks both its colour and its shape. */
export const stateTone: Record<PresentationState, Tone> = {
  working: 'working',
  'waiting-agents': 'waiting',
  'waiting-ci': 'waiting',
  'waiting-tools': 'waiting',
  waiting: 'ended',
  answered: 'ended',
  'needs-you': 'needs-you',
  ready: 'review',
  failed: 'failed',
  disconnected: 'disconnected',
  completed: 'done',
  merged: 'done',
  closed: 'closed',
  interrupted: 'interrupted',
  paused: 'paused',
  running: 'neutral',
}

export const groupLabel: Record<RunGroup, string> = {
  'needs-you': 'Needs you',
  working: 'Working',
  review: 'Ready for review',
  finished: 'Finished',
}
