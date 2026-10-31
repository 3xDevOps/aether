import type { Tone } from '@/components/ui/status-dot'
import {
  awaitsCompletion,
  isPaused,
  memberName,
  needsYou,
  waitingOn,
  type NeedsYouCondition,
  type StateContext,
} from '@/lib/needs-you'
import { clockTime, compactAge, timeAgo } from '@/lib/format'
import { plainReason, possibleStallPrefix } from '@/lib/reasons'
import type { BackgroundTask } from '@/lib/session-types'
import type { RunActivity, RunStatus, RunStatusCause, RunWait } from '@/lib/types'
import { isTerminal, type RunRecord } from '@/store/runs'

/**
 * A live run's state; one without a state is shown by its status. `ended` is the wire's `waiting`, a
 * turn that ended with no report; `waiting` is a run waiting on a member other than the viewer.
 */
type LiveState = Exclude<RunActivity, 'idle' | 'finished'> | 'ended' | 'ready' | 'running'

type Outcome = 'completed' | 'merged' | 'closed' | 'interrupted'

/**
 * A live run's state, a finished run's outcome, or Paused. `stalled` is the wire's `waiting` parked by a
 * stall whose reason names the turn's phase: the turn may still be in flight, so it did not end.
 */
export type PresentationState = LiveState | Outcome | 'paused' | 'stalled'

export type RunGroup = 'needs-you' | 'working' | 'review' | 'finished'

export interface RunPresentation {
  state: PresentationState
  reason: string
  /** What happens next, or what the viewer can do; absent when nothing is to say. */
  next?: string
  needsYou?: NeedsYouCondition
  /** What chose the server's state, from the run's `cause`; absent once the run is not live. */
  cause?: RunStatusCause
  /** What Aether verified about the wait the server's state shows. */
  wait?: RunWait
}

export function backgroundReason(tasks: BackgroundTask[]): string {
  const held = tasks.filter((task) => task.holds).length
  return held === 0 ? 'Working in background' : `Working in background: ${held} ${held === 1 ? 'task' : 'tasks'}`
}

/** A parked Enhanced run whose stall named the turn's phase: it may yet make progress. */
export function possiblyStalled(run: Pick<RunRecord, 'status' | 'reason'>): boolean {
  return run.status === 'needs-attention' && run.reason?.startsWith(possibleStallPrefix) === true
}

const liveActivities = new Set<string>(['working', 'waiting-agents', 'waiting-ci', 'waiting-tools', 'answered', 'needs-you', 'failed', 'disconnected'])

/**
 * A live run's state from its wire status and activity. A reported success reads Ready for review.
 * A parked run with no known state, or the `idle` older servers wrote, ended its turn without a
 * report; a running one this dashboard cannot read is shown as Running.
 */
export function liveStateOf(status: 'running' | 'needs-attention', state: string | undefined): LiveState {
  if (state === 'finished') return 'ready'
  if (state === 'idle' || state === 'waiting') return 'ended'
  if (state && liveActivities.has(state)) return state as LiveState
  return status === 'needs-attention' ? 'ended' : 'running'
}

const stateReasons: Record<LiveState, string> = {
  working: 'Agent working',
  'waiting-agents': 'Waiting for its agents to report',
  'waiting-ci': 'Waiting for CI',
  'waiting-tools': 'Waiting for a background process',
  ended: 'Turn ended without a report',
  waiting: 'Waiting',
  answered: 'Answered',
  'needs-you': 'The agent needs you',
  ready: 'Agent reported success',
  failed: 'Agent reported failure',
  disconnected: 'Agent disconnected',
  running: 'Running',
}

const resumes = 'It resumes on its next message.'

/** What happens next. Only a state that asks something of the viewer says what to do. */
const stateNext: Partial<Record<PresentationState, string>> = {
  'waiting-agents': 'Mail from its agents can wake it.',
  'waiting-ci': resumes,
  'waiting-tools': resumes,
  ready: 'Mark it complete, or send a message to keep going.',
  stalled: "It returns to Working with the agent's next update.",
}

function liveState(run: RunRecord, ctx: StateContext): Omit<RunPresentation, 'needsYou'> {
  if (run.status === 'queued') return { state: 'working', reason: 'Queued' }
  if (run.status === 'provisioning') return { state: 'working', reason: 'Starting' }
  const state = liveStateOf(run.status as 'running' | 'needs-attention', run.state)
  const waiter = waitingOn(run, ctx)
  if (waiter !== undefined) {
    return { state: state === 'needs-you' || state === 'ended' ? 'waiting' : state, reason: `Waiting for ${memberName(waiter, ctx)}` }
  }
  if (state === 'working' && run.toolActivity) {
    return { state, reason: `${run.toolActivity.verb} ${run.toolActivity.target}`.trimEnd() }
  }
  const reason = run.reason && run.reason !== 'agent resumed' ? plainReason(run.reason) : stateReasons[state]
  if (possiblyStalled(run)) return { state: 'stalled', reason, next: stateNext.stalled }
  if (state === 'failed') return { state, reason: `${reason}. Session remains open for follow-up.` }
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
  const shown = presentation(run, ctx)
  // The cause and the wait explain the server's state only: none where the dashboard shows another.
  // A request the viewer answers still shows the wait; one that waits on someone else names them instead.
  const served = !isTerminal(run.status) &&
    (shown.state === liveStateOf(run.status as 'running' | 'needs-attention', run.state) || shown.state === 'stalled')
  const own = served && waitingOn(run, ctx) === undefined
  const wait = run.wait && (own || (served && shown.needsYou !== undefined))
  return { ...shown, ...(own && run.cause && { cause: run.cause }), ...(wait && { wait: run.wait }) }
}

function presentation(run: RunRecord, ctx: StateContext): RunPresentation {
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
  return liveState(run, ctx)
}

/** The reason worth printing beside the state's label, without the words that restate it. */
export function stateDetail({ state, reason }: Pick<RunPresentation, 'state' | 'reason'>): string | undefined {
  const label = stateLabel[state]
  if (reason === label || reason === stateReasons[state as LiveState]) return undefined
  return reason.startsWith(`${label}: `) ? reason.slice(label.length + 2) : reason
}

/** A broken wait that needs the viewer already names what broke in its reason, so it adds only the error. */
export function waitLine(wait: RunWait | undefined, state: PresentationState, now = Date.now()): string | undefined {
  if (!wait?.detail || wait.kind === 'unverified') return undefined
  const error = wait.error ? ` — ${wait.error}` : ''
  if (wait.kind === 'system_fault') return state === 'needs-you' ? wait.error || undefined : `Wait fault: ${wait.detail}${error}`
  const at = wait.verified_at
  if (wait.kind === 'source_unknown') return `Waiting on ${wait.detail}${at ? ` since ${clockTime(at, now)}` : ''}${error}`
  const age = at && compactAge(at, now)
  return `Waiting on ${wait.detail}${age ? ` · checked ${age === 'now' ? 'just now' : `${age} ago`}` : ''}${error}`
}

const staleWait = /^No update for ([^\s:]+)(?:: (.*))?$/s

/**
 * A state's words: what the run waits on when the reason names it ("Waiting for 2 child runs",
 * "Waiting for Ada") and no checked wait says more, else the state's label, and the detail after
 * it. Given `since`, a CI wait's detail starts with how long it has run, for a surface that prints
 * no age of its own, unless it went stale.
 */
export function stateWords(
  shown: Pick<RunPresentation, 'state' | 'reason' | 'cause' | 'wait'>,
  since?: string,
  now = Date.now(),
): { label: string; detail?: string } {
  let reason = stateDetail(shown)
  const waited = waitLine(shown.wait, shown.state, now)
  if (!waited && (shown.state === 'waiting-agents' || shown.state === 'waiting') && reason?.startsWith('Waiting for ')) return { label: reason }
  const stale = shown.cause === 'wait-expired' ? staleWait.exec(shown.reason) : null
  if (stale) {
    const [, span, summary] = stale
    reason = `Reported waiting${summary ? ` on ${summary}` : ''}, no update for ${span}`
  }
  if (waited) reason = shown.state === 'needs-you' ? `${reason ?? shown.reason} — ${waited}` : waited
  const label = stateName(shown)
  // A stale wait's reason already says how long nothing happened.
  const ran = since && shown.state === 'waiting-ci' && !stale ? compactAge(since, now) : undefined
  return { label, detail: [ran, reason].filter(Boolean).join(' · ') || undefined }
}

export function stateName({ state, cause }: Pick<RunPresentation, 'state' | 'cause'>): string {
  return state === 'ended' && cause === 'wait-expired' ? 'Stale wait' : stateLabel[state]
}

export function toneOf({ state, wait }: Pick<RunPresentation, 'state' | 'wait'>): Tone {
  if (state !== 'needs-you' && wait?.kind === 'source_unknown') return 'wait-unknown'
  if (state !== 'needs-you' && wait?.kind === 'system_fault') return 'wait-fault'
  return stateTone[state]
}

// Mirrors events.RunStatusCause in internal/events/event.go.
const causeWords: Record<string, string> = {
  turn: 'a turn is running',
  report: 'the agent reported it',
  'turn-end': 'the turn ended',
  stall: 'the session stalled',
  'wait-expired': 'its reported wait went stale',
  'wait-fault': 'a condition it waits on resolved or broke, and the agent was not woken',
  workers: 'its subsessions are live',
  'tasks-held': 'it holds background tasks',
  human: 'a request waits on a person',
  disconnect: 'the agent exited',
}

/**
 * The tooltip every surface gives a run's state: the state, since when, the reason the server
 * gave, what Aether verified about a wait with the exact time of its last read, what happens
 * next, and what chose the state.
 */
export function stateTitle(shown: Pick<RunPresentation, 'state' | 'reason' | 'next' | 'cause' | 'wait'>, since: string, now = Date.now()): string {
  const { label, detail } = stateWords(shown, undefined, now)
  const ago = timeAgo(since, now)
  const head = ago ? `${label} since ${ago}` : label
  const said = detail ? ` — ${detail.replace(/\.$/, '')}.` : '.'
  const { wait } = shown
  const read = wait?.verified_at && wait.kind !== 'unverified' ? `Last read ${new Date(wait.verified_at).toLocaleString()}.` : undefined
  const summary = waitLine(wait, shown.state, now) && shown.state !== 'needs-you' ? stateDetail(shown) : undefined
  const reported = summary ? `The agent reported: ${summary.replace(/\.$/, '')}.` : undefined
  const unverified = wait?.kind === 'unverified' ? 'Not verified: the wait names no condition Aether can check.' : undefined
  const why = shown.cause ? `Why: ${causeWords[shown.cause] ?? shown.cause}.` : undefined
  return [`${head}${said}`, read, reported, unverified, shown.next, why].filter(Boolean).join(' ')
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
  return line ? clipLabel(line) : 'Untitled session'
}

export const stateLabel: Record<PresentationState, string> = {
  working: 'Working',
  'waiting-agents': 'Waiting for agents',
  'waiting-ci': 'Waiting for CI',
  'waiting-tools': 'Waiting for process',
  ended: 'Turn ended',
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
  stalled: 'Possibly stalled',
}

/** Each state's dot: the tone picks both its colour and its shape. */
export const stateTone: Record<PresentationState, Tone> = {
  working: 'working',
  'waiting-agents': 'waiting',
  'waiting-ci': 'waiting',
  'waiting-tools': 'waiting',
  ended: 'ended',
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
  stalled: 'waiting',
}

export const groupLabel: Record<RunGroup, string> = {
  'needs-you': 'Needs you',
  working: 'Working',
  review: 'Ready for review',
  finished: 'Finished',
}
