import type { Tone } from '@/components/ui/status-dot'
import {
  isPaused,
  memberName,
  needsYou,
  supervised,
  unreadMail,
  unreadReason,
  waitingOn,
  type NeedsYouCondition,
  type StateContext,
} from '@/lib/needs-you'
import { plainReason } from '@/lib/reasons'
import type { BackgroundTask } from '@/lib/session-types'
import type { RunActivity } from '@/lib/types'
import { isTerminal, type RunRecord } from '@/store/runs'

/** A live run's state; one without a state is shown by its status. */
type LiveState = RunActivity | 'running' | 'needs-attention'

/** A live run's state, a finished run's outcome, or Paused. */
export type PresentationState = LiveState | 'paused'

export type RunGroup = 'needs-you' | 'working' | 'finished'

export interface RunPresentation {
  state: PresentationState
  reason: string
  needsYou?: NeedsYouCondition
  unread?: number
}

export function backgroundReason(tasks: BackgroundTask[]): string {
  const held = tasks.filter((task) => task.holds).length
  return held === 0 ? 'Working in background' : `Working in background: ${held} ${held === 1 ? 'task' : 'tasks'}`
}

/** A run or stored event from before the server derived a state, or one with a state this dashboard does not know, is shown by its status. */
export function liveStateOf(status: 'running' | 'needs-attention', state: string | undefined): LiveState {
  return state && state !== status && state in stateReasons ? (state as RunActivity) : status
}

const stateReasons: Record<LiveState, string> = {
  working: 'Agent working',
  'waiting-agents': 'Waiting on its agents',
  'waiting-tools': 'Waiting on a tool',
  'needs-you': 'The agent needs you',
  idle: 'Agent idle',
  finished: 'Agent reported success',
  failed: 'Agent reported failure',
  disconnected: 'Agent disconnected',
  running: 'Running',
  'needs-attention': 'Parked',
}

function liveState(run: RunRecord, ctx: StateContext): { state: PresentationState; reason: string } {
  if (run.status === 'queued') return { state: 'working', reason: 'Queued' }
  if (run.status === 'provisioning') return { state: 'working', reason: 'Starting' }
  const state = liveStateOf(run.status as 'running' | 'needs-attention', run.state)
  const waiter = waitingOn(run, ctx)
  if (waiter !== undefined) return { state: state === 'needs-you' ? 'idle' : state, reason: `Waiting for ${memberName(waiter, ctx)}` }
  if (state === 'needs-you' && supervised(run, ctx)) return { state: 'waiting-agents', reason: 'Waiting for the integrator' }
  if (state === 'working' && run.toolActivity) {
    return { state, reason: `${run.toolActivity.verb} ${run.toolActivity.target}`.trimEnd() }
  }
  const reason = run.reason && run.reason !== 'agent resumed' ? plainReason(run.reason) : stateReasons[state]
  if (state === 'finished' || state === 'failed') return { state, reason: `${reason}. Run remains open for follow-up.` }
  return { state, reason }
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
      return run.reason ? `Interrupted: ${plainReason(run.reason)}` : 'Interrupted'
    default:
      return run.reason ? `Failed: ${plainReason(run.reason)}` : 'Failed'
  }
}

export function presentRun(run: RunRecord, ctx: StateContext): RunPresentation {
  const condition = needsYou(run, ctx)
  if (condition) return { state: condition.state, reason: condition.reason(run, ctx), needsYou: condition }
  if (isTerminal(run.status)) {
    return {
      state: run.status === 'failed' || run.status === 'interrupted' ? 'failed' : 'finished',
      reason: finishedReason(run),
    }
  }
  if (isPaused(run, ctx)) return { state: 'paused', reason: 'Paused' }
  const live = liveState(run, ctx)
  const unread = unreadMail(run, ctx.now)
  if (unread > 0 && waitingOn(run, ctx) === undefined) {
    return { state: live.state, reason: unreadReason(unread, run.oldest_unacked_at!, ctx.now), unread }
  }
  return live
}

/** A row lists in Needs you when a condition applies to the viewer, whatever its label. */
export function groupOf({ state, needsYou }: Pick<RunPresentation, 'state' | 'needsYou'>): RunGroup {
  if (needsYou) return 'needs-you'
  return state === 'finished' || state === 'failed' ? 'finished' : 'working'
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

export function runLabel(run: { task: string; title?: string }): string {
  const title = run.title?.trim()
  if (title) return title
  const line = run.task.split('\n').find((l) => l.trim())?.trim() ?? ''
  return line ? clipLabel(line) : 'Untitled run'
}

export const stateLabel: Record<PresentationState, string> = {
  working: 'Working',
  'waiting-agents': 'Waiting on agents',
  'waiting-tools': 'Waiting on tools',
  'needs-you': 'Needs you',
  idle: 'Idle',
  finished: 'Finished',
  failed: 'Failed',
  disconnected: 'Disconnected',
  paused: 'Paused',
  running: 'Running',
  'needs-attention': 'Parked',
}

/** Each state's dot: the tone picks both its colour and its shape. */
export const stateTone: Record<PresentationState, Tone> = {
  working: 'working',
  'waiting-agents': 'waiting',
  'waiting-tools': 'waiting',
  'needs-you': 'needs-you',
  idle: 'idle',
  finished: 'done',
  failed: 'failed',
  disconnected: 'disconnected',
  paused: 'paused',
  running: 'neutral',
  'needs-attention': 'neutral',
}

export const groupLabel: Record<RunGroup, string> = {
  'needs-you': 'Needs you',
  working: 'Working',
  finished: 'Finished',
}
