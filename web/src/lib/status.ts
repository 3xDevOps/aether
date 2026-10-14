import {
  isPaused,
  memberName,
  needsYou,
  stopReasons,
  stoppedBecause,
  reportedOutcome,
  supervised,
  unreadMail,
  unreadReason,
  waitingOn,
  type NeedsYouCondition,
  type StateContext,
} from '@/lib/needs-you'
import type { BackgroundTask } from '@/lib/session-types'
import { isTerminal, type RunRecord } from '@/store/runs'

export type PresentationState = 'needs-you' | 'working' | 'paused' | 'done' | 'failed'

export type RunGroup = 'needs-you' | 'working' | 'finished'

export interface RunPresentation {
  state: PresentationState
  reason: string
  needsYou?: NeedsYouCondition
  unread?: number
}

// Mirrors acpBackgroundReason in internal/scheduler/acp_turn.go.
const backgroundPrefix = 'working in background'

export function backgroundReason(tasks: BackgroundTask[]): string {
  const held = tasks.filter((task) => task.holds).length
  return held === 0 ? 'Working in background' : `Working in background: ${held} ${held === 1 ? 'task' : 'tasks'}`
}

function workingReason(run: RunRecord, ctx: StateContext): string {
  if (run.status === 'queued') return 'Queued'
  if (run.status === 'provisioning') return 'Starting'
  const waiter = waitingOn(run, ctx)
  if (waiter !== undefined) return `Waiting for ${memberName(waiter, ctx)}`
  if (supervised(run, ctx) && run.status === 'needs-attention') return 'Waiting for the integrator'
  if (run.status === 'running' && run.reason?.startsWith(backgroundPrefix)) return plainReason(run.reason)
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
      return run.reason ? `Interrupted: ${plainReason(run.reason)}` : 'Interrupted'
    default:
      return run.reason ? `Failed: ${plainReason(run.reason)}` : 'Failed'
  }
}

const turnEnds: Record<string, string> = {
  end_turn: 'The agent finished its turn',
  cancelled: 'The turn was cancelled',
}

const savedResults = "the worker's saved results"

// Mirrors the reason strings internal/scheduler, internal/agentstatus and internal/coord write.
const plainReasons: [RegExp, (...groups: string[]) => string][] = [
  [/^(.*); retained container$/, (rest) => plainReason(rest)],
  [/^agent reported success$/, () => 'Agent reported success'],
  [/^agent reported failure$/, () => 'Agent reported failure'],
  [/^agent exited; results committed$/, () => 'Agent exited and its changes were committed'],
  [/^agent exited (-?\d+)(.*)$/s, (code, rest) => `Agent exited with code ${code}${rest}`],
  [/^worker finished$/, () => 'Worker finished'],
  [/^closed$/, () => 'Closed'],
  [/^killed$/, () => 'Stopped'],
  [/^retained container expired$/, () => 'Its container was removed when it expired'],
  [/^retained container unavailable$/, () => 'Its container is no longer available'],
  [/^server restarted: container exited (-?\d+)$/, (code) => `Its container had stopped with exit code ${code} when the server restarted`],
  [/^agent idle$/, () => 'Agent idle'],
  [/^agent resumed$/, () => 'Agent resumed'],
  [/^agent failed$/, () => 'Agent failed'],
  [/^stalled: no output or file changes for (.+)$/, (span) => `No output or file changes for ${span}`],
  [/^blocked: (.*)$/s, (why) => `Blocked: ${why}`],
  [/^needs input: (.*)$/s, (question) => `Agent asks: ${question}`],
  [/^working in background(: \d+ tasks?)?$/, (count = '') => `Working in background${count}`],
  [/^turn cancelled$/, () => 'Turn cancelled'],
  [/^turn interrupted$/, () => 'Turn interrupted'],
  [/^turn ended: (\w+)$/, (stop) => (stop in stopReasons ? stoppedBecause(stop) : turnEnds[stop] ?? `The agent's turn ended (${stop})`)],
  [/^the agent's turn ended: (\w+)$/, (stop) => (stop in stopReasons ? stoppedBecause(stop) : turnEnds[stop] ?? `The agent's turn ended (${stop})`)],
  [/^send the task to the agent: (.*)$/s, (err) => `Could not send the task to the agent: ${err}`],
  [/^review retained evidence$/, () => `Review ${savedResults}`],
  [/^resolve the blocker using the retained evidence$/, () => `Resolve the blocker using ${savedResults}`],
  [/^retained evidence (?:is )?unavailable(.*)$/s, (rest) => `The worker's saved results are unavailable${rest}`],
]

export function plainReason(reason: string): string {
  for (const [pattern, words] of plainReasons) {
    const match = pattern.exec(reason)
    if (match) return words(...match.slice(1))
  }
  return reason
}

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
  const unread = unreadMail(run, ctx.now)
  if (unread > 0 && waitingOn(run, ctx) === undefined) {
    return { state: 'working', reason: unreadReason(unread, run.oldest_unacked_at!, ctx.now), unread }
  }
  const outcome = reportedOutcome(run)
  if (outcome) {
    return {
      state: outcome === 'success' ? 'done' : 'failed',
      reason: `Agent reported ${outcome}. Run remains open for follow-up.`,
    }
  }
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
