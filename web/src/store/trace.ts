import type { Tone } from '@/components/ui/status-dot'
import { isReread } from '@/lib/events'
import type { SessionItem, SessionRequest, SessionToolCall } from '@/lib/session-types'
import { liveStateOf, outcomeState, stateLabel, stateTone } from '@/lib/status'
import type { Event, RunStatus, RunStatusPayload } from '@/lib/types'
import { isLaunch, quietDuration, stopText, toolLabel, toolStatus, wakeText, type Turn } from '@/store/session-rows'
import { agentLabel, agentState, agentType, settles, type AgentIndex, type Subagent } from '@/store/subagents'

export type SpanKind = 'run' | 'turn' | 'agent' | 'tool' | 'wait' | 'notice' | 'workers' | 'worker'

/** stopped: still open when its turn or agent ended, or cancelled. */
export type SpanStatus = 'running' | 'done' | 'failed' | 'stopped' | 'waiting'

export interface Interval {
  from: number
  to: number
}

export interface LaneSegment {
  from: number
  /** Null while it lasts. */
  to: number | null
  tone: Tone
  label: string
}

export interface Span {
  id: string
  kind: SpanKind
  label: string
  sub?: string
  /** The tool kind, for its glyph. */
  tool?: string
  start: number
  /** Null while it runs. A parent reaches its last child's end. */
  end: number | null
  status: SpanStatus
  children: Span[]
  /** Spans below it. */
  size: number
  /** Thinking and reply intervals inside a turn or subagent bar. */
  model?: Interval[]
  /** The run's state over time. */
  lane?: LaneSegment[]
  /** The start is the first held item, not the launch. */
  unknownStart?: boolean
  turn?: number
  callID?: string
  requestID?: string
  /** The subagent this span is, or runs inside. */
  agentID?: string
  runID?: string
  command?: string
  exitCode?: number
  path?: string
  diffs?: number
  /** The stop reason in Session words. */
  stop?: string
  autonomous?: boolean
  context?: { used: number; size: number }
  request?: SessionRequest['kind']
  requestStatus?: SessionRequest['status']
  answer?: string
  /** On the run: why its status history could not be read. */
  laneError?: string
}

export interface WorkerSource {
  runID: string
  label: string
  start: number
  end: number | null
  status: SpanStatus
}

export interface TraceSources {
  start: number
  end: number | null
  runStatus: RunStatus
  runState?: string
  live: boolean
  turns: Turn[]
  agents: AgentIndex
  /** Null while the status history is not held. */
  statusEvents: Event[] | null
  statusError?: string | null
  workers?: WorkerSource[]
}

const time = (item: SessionItem) => Date.parse(item.time)

/** A span's end on the axis, with a running one reaching now. */
export function spanEnd(span: Span, now: number): number {
  return span.end ?? Math.max(now, span.start)
}

function reach(children: Span[], own: number | null): number | null {
  let end = own
  for (const child of children) {
    if (child.end === null) return null
    if (end !== null && child.end > end) end = child.end
  }
  return end
}

function sized(span: Omit<Span, 'size'>): Span {
  return { ...span, size: span.children.reduce((sum, child) => sum + 1 + child.size, 0) }
}

const byStart = (a: Span, b: Span) => a.start - b.start

interface Owner {
  /** When the owning turn or subagent ended; open calls stop there. Null while it runs. */
  ended: number | null
  live: boolean
  prefix: string
  turn?: number
  agentID?: string
}

const taskStatus: Record<NonNullable<SessionToolCall['task']>['status'], SpanStatus> = {
  running: 'running',
  completed: 'done',
  failed: 'failed',
  stopped: 'stopped',
}

function settledStatus(call: SessionToolCall): SpanStatus {
  if (call.task) return taskStatus[call.task.status]
  return toolStatus(call) === 'failed' ? 'failed' : 'done'
}

interface Track {
  call: SessionToolCall
  first: SessionItem
  last: SessionItem
  settled?: SessionItem
}

/** Tool calls, waits, error notices and model time from one turn's or subagent's own items. */
function itemSpans(items: SessionItem[], owner: Owner): { spans: Span[]; model: Interval[] } {
  const calls = new Map<string, Track>()
  const waits = new Map<string, { first: SessionItem; last: SessionItem }>()
  const messages = new Map<string, Interval>()
  const spans: Span[] = []
  for (const item of items) {
    const call = item.tool_call
    if (item.kind === 'tool_call' && call) {
      const track = calls.get(call.id)
      if (!track) calls.set(call.id, { call, first: item, last: item, settled: settles(call) ? item : undefined })
      else {
        track.call = call
        track.last = item
        if (!track.settled && settles(call)) track.settled = item
      }
    } else if (item.kind === 'request' && item.request) {
      const held = waits.get(item.request.id)
      if (held) held.last = item
      else waits.set(item.request.id, { first: item, last: item })
    } else if ((item.kind === 'message' || item.kind === 'thought') && item.message && item.message.role !== 'user') {
      const key = `${item.kind}:${item.message.message_id}`
      const at = time(item)
      const held = messages.get(key)
      if (held) held.to = at
      else messages.set(key, { from: at, to: at })
    } else if (item.kind === 'notice' && item.notice?.severity === 'error') {
      const at = time(item)
      spans.push({ id: `${owner.prefix}notice:${item.seq}`, kind: 'notice', label: item.notice.title, start: at, end: at, status: 'failed', children: [], size: 0, turn: owner.turn, agentID: owner.agentID })
    }
  }
  for (const { call, first, last, settled } of calls.values()) {
    if (isLaunch(call)) continue
    const start = time(first)
    let status: SpanStatus = settled ? settledStatus(settled.tool_call!) : 'running'
    let end: number | null = settled ? time(settled) : null
    if (!settled && owner.ended !== null && !(call.task && owner.live)) {
      status = 'stopped'
      end = Math.max(owner.ended, time(last))
    }
    if (end !== null && end - start < quietDuration) end = start
    spans.push({
      id: `${owner.prefix}call:${call.id}`,
      kind: 'tool',
      label: toolLabel(call, status === 'running' ? 'running' : 'done'),
      sub: call.tool_name,
      tool: call.tool_kind ?? 'other',
      start,
      end,
      status,
      children: [],
      size: 0,
      turn: owner.turn,
      agentID: owner.agentID,
      callID: call.id,
      command: call.tool_kind === 'execute' ? call.title : undefined,
      exitCode: call.exit_code,
      path: call.locations?.[0]?.path ?? call.diffs?.[0]?.path,
      diffs: call.diffs?.length || undefined,
    })
  }
  for (const [id, { first, last }] of waits) {
    const request = last.request!
    const pending = request.status === 'pending'
    const open = pending && owner.ended === null
    spans.push({
      id: `${owner.prefix}wait:${id}`,
      kind: 'wait',
      label: request.title || 'Waiting for you',
      sub: request.kind,
      start: time(first),
      end: open ? null : pending ? Math.max(owner.ended!, time(first)) : time(last),
      status: open ? 'waiting' : request.status === 'cancelled' || pending ? 'stopped' : 'done',
      children: [],
      size: 0,
      turn: owner.turn,
      requestID: id,
      request: request.kind,
      requestStatus: request.status,
      answer: request.answer,
    })
  }
  return { spans, model: [...messages.values()] }
}

const agentStatus: Record<ReturnType<typeof agentState>, SpanStatus> = {
  running: 'running',
  done: 'done',
  launched: 'done',
  failed: 'failed',
  stopped: 'stopped',
  unknown: 'done',
}

const agentCache = new WeakMap<Subagent, { live: boolean; children: Span[]; span: Span }>()

function agentSpan(index: AgentIndex, id: string, live: boolean, seen: Set<string>): Span | null {
  const agent = index.agents[id]
  if (!agent || seen.has(id)) return null
  seen.add(id)
  const nested = agent.children.flatMap((child) => agentSpan(index, child, live, seen) ?? [])
  const cached = agentCache.get(agent)
  if (cached && cached.live === live && cached.children.length === nested.length && cached.children.every((child, i) => child === nested[i])) return cached.span
  const state = agentState(agent)
  const status = !live && state === 'running' ? 'stopped' : agentStatus[state]
  const first = agent.items[0]
  const startedAt = agent.startedAt ?? first?.time ?? agent.settledAt ?? agent.latest?.time
  if (!startedAt) return null
  const start = Date.parse(startedAt)
  const latest = agent.latest ? time(agent.latest) : start
  const settled = agent.settledAt ? Date.parse(agent.settledAt) : undefined
  const own = status === 'running'
    ? null
    : status === 'stopped'
      ? Math.max(agent.turnEndedAt ? Date.parse(agent.turnEndedAt) : latest, start)
      : Math.max(settled ?? latest, agent.workflow || agent.background ? latest : start)
  const { spans, model } = itemSpans(agent.items, { ended: own, live, prefix: `${id}/`, turn: agent.turn, agentID: id })
  const children = [...spans, ...nested].sort(byStart)
  const span = sized({
    id: `agent:${id}`,
    kind: 'agent',
    label: agentLabel(agent),
    sub: agentType(agent),
    start,
    end: reach(children, own),
    status,
    children,
    model,
    unknownStart: !agent.startedAt,
    agentID: id,
    turn: agent.turn,
  })
  agentCache.set(agent, { live, children: nested, span })
  return span
}

function promptOf(turn: Turn): string | undefined {
  const prompt = turn.items.find((item) => item.kind === 'message' && item.message?.role === 'user')?.message?.text
  if (prompt === undefined) return undefined
  return wakeText(prompt) ?? prompt.split('\n').map((line) => line.trim()).find(Boolean)?.slice(0, 160)
}

interface TurnInput {
  turn: Turn
  agents: Span[]
  live: boolean
  last: boolean
}

const turnCache = new WeakMap<Turn, { input: TurnInput; span: Span | null }>()

function sameInput(a: TurnInput, b: TurnInput): boolean {
  return a.live === b.live && a.last === b.last && a.agents.length === b.agents.length && a.agents.every((agent, i) => agent === b.agents[i])
}

function turnSpan(input: TurnInput): Span | null {
  const cached = turnCache.get(input.turn)
  if (cached && sameInput(cached.input, input)) return cached.span
  const span = deriveTurn(input)
  turnCache.set(input.turn, { input, span })
  return span
}

function deriveTurn({ turn, agents, live, last }: TurnInput): Span | null {
  const started = turn.items.find((item) => item.kind === 'turn_start')
  const ended = turn.items.find((item) => item.kind === 'turn_end')
  const first = started ?? turn.items[0]
  if (!first || (!started && turn.turn === 0 && agents.length === 0 && !turn.items.some((item) => item.kind === 'tool_call'))) return null
  const start = time(first)
  const lastItem = turn.items.at(-1)!
  // Turn 0 holds items from before the first turn; a numbered turn runs even when its turn_start is not held yet.
  const running = !ended && !turn.closed && last && live && (started !== undefined || turn.turn > 0)
  const own = ended ? time(ended) : running ? null : time(lastItem)
  let status: SpanStatus = running ? 'running' : 'stopped'
  let stop: string | undefined
  if (ended) {
    const [text, tone] = stopText[ended.stop_reason ?? ''] ?? [`Stopped: ${ended.stop_reason ?? 'unknown reason'}`, 'failed']
    stop = text
    status = tone === 'done' ? 'done' : tone === 'paused' ? 'stopped' : 'failed'
  }
  const { spans, model } = itemSpans(turn.items, { ended: own, live, prefix: `${turn.turn}/`, turn: turn.turn })
  const children = [...spans, ...agents].sort(byStart)
  let usage: SessionItem['usage']
  for (const item of turn.items) if (item.kind === 'usage' && item.usage) usage = item.usage
  const label = promptOf(turn) ?? `Turn ${turn.turn}`
  return sized({
    id: `turn:${turn.turn}`,
    kind: 'turn',
    label,
    sub: label === `Turn ${turn.turn}` ? undefined : `Turn ${turn.turn}`,
    start,
    end: reach(children, own),
    status,
    children,
    model,
    unknownStart: !started,
    turn: turn.turn,
    stop,
    autonomous: started?.autonomous === true,
    context: usage && usage.size > 0 ? { used: usage.used, size: usage.size } : undefined,
  })
}

function laneTone(to: RunStatus, state: string | undefined): { tone: Tone; label: string } {
  if (to === 'queued' || to === 'provisioning') return { tone: 'working', label: to === 'queued' ? 'Queued' : 'Starting' }
  const presentation = outcomeState(to) ?? liveStateOf(to as 'running' | 'needs-attention', state)
  return { tone: stateTone[presentation], label: stateLabel[presentation] }
}

function lane(sources: TraceSources, start: number): LaneSegment[] | undefined {
  if (!sources.statusEvents) return undefined
  // A capped read can hold only rereads; the oldest still marks where loaded history starts.
  const changes = sources.statusEvents.filter((event, i) => i === 0 || !isReread(event)).map((event) => ({ at: Date.parse(event.time), ...(event.payload as RunStatusPayload) }))
  if (changes.length === 0) return [{ from: start, to: sources.end, ...laneTone(sources.runStatus, sources.runState) }]
  return changes.map((change, i) => ({
    from: Math.max(change.at, start),
    to: changes[i + 1]?.at ?? sources.end,
    ...laneTone(change.to, change.state),
  }))
}

const workersFoldAbove = 8

/** The span tree of an Enhanced run: the run, its turns, their tool calls, waits and subagents, and an integrator's workers. */
export function deriveTrace(sources: TraceSources): Span {
  const { turns, agents, live } = sources
  const byTurn = new Map<number, Span[]>()
  const seen = new Set<string>()
  // A subagent whose launch is not held is in no parent's list.
  const unlaunched = Object.values(agents.agents).filter((agent) => agent.launchSeq === undefined).map((agent) => agent.id)
  for (const id of [...agents.roots, ...unlaunched]) {
    const span = agentSpan(agents, id, live, seen)
    if (!span) continue
    const turn = agents.agents[id]!.turn ?? agents.agents[id]!.items[0]?.turn ?? 0
    byTurn.set(turn, [...(byTurn.get(turn) ?? []), span])
  }
  const held = new Set(turns.map((turn) => turn.turn))
  const children = [
    ...turns.map((turn, i) => [turn.turn, turnSpan({ turn, agents: byTurn.get(turn.turn) ?? [], live, last: i === turns.length - 1 })] as const),
    ...[...byTurn].flatMap(([turn, spans]) => (held.has(turn) ? [] : spans.map((span) => [turn, span] as const))),
  ].sort(([a], [b]) => a - b).flatMap(([, span]) => span ?? [])
  if (sources.workers?.length) {
    const workers = sources.workers.map((worker): Span => ({
      id: `worker:${worker.runID}`,
      kind: 'worker',
      label: worker.label,
      start: worker.start,
      end: worker.end,
      status: worker.status,
      children: [],
      size: 0,
      runID: worker.runID,
    })).sort(byStart)
    children.push(sized({
      id: 'workers',
      kind: 'workers',
      label: 'Workers',
      sub: String(workers.length),
      start: workers[0]!.start,
      end: reach(workers, workers[0]!.start),
      status: workers.some((worker) => worker.status === 'running') ? 'running' : 'done',
      children: workers,
    }))
  }
  const start = Math.min(sources.start, ...children.map((child) => child.start))
  const failed = sources.runStatus === 'failed' || sources.runStatus === 'interrupted'
  return sized({
    id: 'run',
    kind: 'run',
    label: 'Run',
    start,
    end: sources.end === null ? null : Math.max(sources.end, ...children.map((child) => child.end ?? sources.end!)),
    status: live ? 'running' : failed ? 'failed' : 'done',
    children,
    lane: lane(sources, start),
    laneError: sources.statusError ?? undefined,
  })
}

/** Whether a parent starts unfolded: the run, a running or the last turn, a running subagent, a short worker list. */
export function startsOpen(span: Span, last: boolean): boolean {
  switch (span.kind) {
    case 'run':
      return true
    case 'turn':
      return span.status === 'running' || last
    case 'agent':
      return span.status === 'running'
    case 'workers':
      return span.children.length <= workersFoldAbove
    default:
      return false
  }
}

export function needsAttention(span: Span): boolean {
  return span.status === 'failed' || span.status === 'waiting'
}

/** The failed and waiting spans under a parent, shown even while it is folded. */
export function surfacedBelow(span: Span): Span[] {
  const out: Span[] = []
  const walk = (node: Span) => {
    for (const child of node.children) {
      if (needsAttention(child)) out.push(child)
      walk(child)
    }
  }
  walk(span)
  return out
}

export interface TraceStats {
  turns: number
  tools: number
  subagents: number
  /** Failed spans in tree order. */
  errors: string[]
  /** The longest finished tool call, once three have finished. */
  slowest?: string
}

export function traceStats(root: Span): TraceStats {
  let turns = 0
  let tools = 0
  let subagents = 0
  let finished = 0
  let slowest: Span | undefined
  const errors: string[] = []
  const walk = (span: Span) => {
    if (span.kind === 'turn' && !span.unknownStart) turns++
    if (span.kind === 'agent') subagents++
    if (span.kind === 'tool') {
      tools++
      if (span.end !== null && span.status !== 'stopped') {
        finished++
        if (!slowest || span.end - span.start > slowest.end! - slowest.start) slowest = span
      }
    }
    if (span.status === 'failed' && span.kind !== 'run') errors.push(span.id)
    span.children.forEach(walk)
  }
  walk(root)
  return { turns, tools, subagents, errors, slowest: finished >= 3 && slowest && slowest.end! > slowest.start ? slowest.id : undefined }
}

/** Every parent span's id. */
export function parentIDs(root: Span): string[] {
  const out: string[] = []
  const walk = (span: Span) => {
    if (span.children.length === 0) return
    out.push(span.id)
    span.children.forEach(walk)
  }
  walk(root)
  return out
}

export function findSpan(root: Span, id: string): Span | undefined {
  if (root.id === id) return root
  for (const child of root.children) {
    const found = findSpan(child, id)
    if (found) return found
  }
  return undefined
}

/** The ids from the root down to the span, the span last. */
export function pathOf(root: Span, id: string): string[] | undefined {
  if (root.id === id) return [root.id]
  for (const child of root.children) {
    const path = pathOf(child, id)
    if (path) return [root.id, ...path]
  }
  return undefined
}

export interface CostTotals {
  input: number
  output: number
  cost: number
}

/** run.cost events oldest first: an Enhanced run publishes increments, a headless one running totals. Undefined until one is metered. */
export function costTotals(events: Event[]): CostTotals | undefined {
  let totals: CostTotals | undefined
  for (const event of events) {
    const p = event.payload as { input_tokens?: number; output_tokens?: number; cost_usd?: number; metered?: boolean; increment?: boolean }
    if (!p.metered) continue
    const next = { input: p.input_tokens ?? 0, output: p.output_tokens ?? 0, cost: p.cost_usd ?? 0 }
    totals = p.increment && totals ? { input: totals.input + next.input, output: totals.output + next.output, cost: totals.cost + next.cost } : next
  }
  return totals
}
