import type { Tone } from '@/components/ui/status-dot'
import { isReread } from '@/lib/events'
import { liveStateOf, outcomeState, stateLabel, stateTone, type PresentationState } from '@/lib/status'
import type { Event, RunStatus, RunStatusPayload } from '@/lib/types'
import { isTerminal, type RunRecord } from '@/store/runs'

/** A run's state over a span. History knows only the server's needs-attention, never whom it needed, so it reads "Needed attention". */
export type SegmentState = PresentationState | 'starting' | 'unloaded' | 'needed-attention'

export interface Segment {
  start: number
  end: number
  state: SegmentState
}

export interface StatusChange {
  t: number
  from?: RunStatus
  to: RunStatus
  state?: string
}

export const segmentLabel: Record<SegmentState, string> = {
  ...stateLabel,
  starting: 'Starting',
  unloaded: 'History not loaded',
  'needed-attention': 'Needed attention',
}

/** Each run's `run.status` changes, oldest first. */
export function statusChanges(events: Event[]): Map<string, StatusChange[]> {
  const byRun = new Map<string, StatusChange[]>()
  for (const event of events) {
    if (event.type !== 'run.status' || !event.run_id || isReread(event)) continue
    const p = event.payload as RunStatusPayload
    const list = byRun.get(event.run_id) ?? []
    list.push({ t: Date.parse(event.time), from: p.from, to: p.to, state: p.state || undefined })
    byRun.set(event.run_id, list)
  }
  return byRun
}

function stateOf(status: RunStatus, state?: string): SegmentState {
  if (status === 'queued' || status === 'provisioning') return 'starting'
  if (status === 'running' || status === 'needs-attention') {
    const live = liveStateOf(status, state)
    return live === 'needs-you' ? 'needed-attention' : live
  }
  return outcomeState(status) ?? 'running'
}

export interface SegmentInput {
  run: RunRecord
  /** The viewer's presentation of the run now. */
  shown: PresentationState
  changes: StatusChange[]
  now: number
  /** History before this time was not read. */
  floor: number
}

/**
 * The run's bar from creation to its end or now. Status history sets each
 * span; the span since the last change shows the viewer's own presentation,
 * which alone knows Needs you, Paused and Ready for review. Spans before the
 * history floor are unloaded: `stateChangedAt` moves on status changes only,
 * so it cannot vouch for activity changes the unread history may hold.
 */
export function runSegments({ run, shown, changes, now, floor }: SegmentInput): Segment[] {
  const created = Date.parse(run.created_at)
  const finished = run.finished_at ? Date.parse(run.finished_at) : undefined
  const end = Math.max(created, finished ?? now)
  const live = !isTerminal(run.status)
  const current: SegmentState = run.status === 'queued' || run.status === 'provisioning' ? 'starting' : shown
  const clamp = (t: number) => Math.min(end, Math.max(created, t))
  const inside = changes.filter((change) => change.t >= created - 1000 && change.t <= end + 1000)
  const points: { t: number; state: SegmentState }[] = [{ t: created, state: 'starting' }]
  const started = run.started_at ? clamp(Date.parse(run.started_at)) : undefined
  if (started !== undefined) {
    const next = inside.find((change) => change.t > started)
    points.push({ t: started, state: next?.from ? stateOf(next.from) : live ? current : 'running' })
  }
  for (const change of inside) points.push({ t: clamp(change.t), state: stateOf(change.to, change.state) })
  const changedAt = clamp(Date.parse(run.stateChangedAt))
  if (live) points.push({ t: Math.max(changedAt, inside.at(-1)?.t ?? created), state: current })
  points.sort((a, b) => a.t - b.t)
  const segments: Segment[] = []
  const push = (start: number, stop: number, state: SegmentState) => {
    if (stop <= start) return
    const last = segments.at(-1)
    if (last && last.state === state && last.end >= start) last.end = stop
    else segments.push({ start, end: stop, state })
  }
  if (floor > created) push(created, Math.min(floor, end), 'unloaded')
  points.forEach((point, i) => {
    const stop = i + 1 < points.length ? points[i + 1].t : end
    push(Math.max(point.t, floor), stop, point.state)
  })
  return segments
}

const outcomeTone: Partial<Record<PresentationState, Tone>> = {
  completed: 'done',
  merged: 'done',
  failed: 'failed',
  interrupted: 'failed',
  closed: 'closed',
}

/** A finished run's working spans take its outcome's colour, so only live work reads blue. */
export function segmentTone(state: SegmentState, outcome?: PresentationState): Tone | 'starting' | 'unloaded' {
  if (state === 'starting' || state === 'unloaded') return state
  if (state === 'needed-attention') return 'needs-you'
  const tone = stateTone[state]
  if (outcome && (tone === 'working' || tone === 'neutral')) return outcomeTone[outcome] ?? tone
  return tone
}

export interface Stretch {
  start: number
  end: number
  tone: Tone
}

/**
 * One strip from many: each instant takes the highest-ranked tone among the
 * stretches covering it. Unranked tones leave a gap.
 */
export function strongest(stretches: Stretch[], rank: Partial<Record<Tone, number>>): Stretch[] {
  const marks: { t: number; tone: Tone; delta: number }[] = []
  for (const s of stretches) {
    if (!rank[s.tone] || s.end <= s.start) continue
    marks.push({ t: s.start, tone: s.tone, delta: 1 }, { t: s.end, tone: s.tone, delta: -1 })
  }
  marks.sort((a, b) => a.t - b.t)
  const open = new Map<Tone, number>()
  const out: Stretch[] = []
  let since = 0
  let top: Tone | undefined
  for (let i = 0; i < marks.length; ) {
    const t = marks[i].t
    for (; i < marks.length && marks[i].t === t; i++) open.set(marks[i].tone, (open.get(marks[i].tone) ?? 0) + marks[i].delta)
    let next: Tone | undefined
    for (const [tone, count] of open) if (count > 0 && (!next || rank[tone]! > rank[next]!)) next = tone
    if (next === top) continue
    if (top) out.push({ start: since, end: t, tone: top })
    top = next
    since = t
  }
  return out
}
