import type { Tone } from '@/components/ui/status-dot'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { Event, RoomMessage, RoomMessageState, RunInputRequest } from '@/lib/types'
import type { RootStore } from '@/store'
import { toolTenses, type AgentPayload } from '@/store/activity'
import { isTerminal, type RunRecord } from '@/store/runs'
import type { SliceCreator } from '@/store/slice'

export const sessionEventTypes = ['run.agent', 'workspace.timeline', 'run.status']

const residentEvents = 2000
const pageSize = 1000

export interface SessionLog {
  events: Event[]
  cursor: number
  loading: boolean
  error: string | null
}

export interface SessionsSlice {
  sessionLogs: Record<string, SessionLog>
  beginSessionLog: (runID: string) => void
  addSessionEvents: (runID: string, events: Event[], cursor?: number) => void
  setSessionLogError: (runID: string, error: string | null) => void
  appendSessionEvent: (event: Event) => void
}

const emptyLog: SessionLog = { events: [], cursor: 0, loading: false, error: null }

function merge(current: Event[], incoming: Event[]): Event[] {
  const seen = new Set(current.map((event) => event.seq))
  const fresh = incoming.filter((event) => !seen.has(event.seq))
  if (fresh.length === 0) return current
  return [...current, ...fresh].sort((a, b) => a.seq - b.seq).slice(-residentEvents)
}

export const createSessionsSlice: SliceCreator<SessionsSlice> = (set, get) => ({
  sessionLogs: {},
  beginSessionLog: (runID) =>
    set((s) => ({
      sessionLogs: { ...s.sessionLogs, [runID]: { ...(s.sessionLogs[runID] ?? emptyLog), loading: true, error: null } },
    })),
  addSessionEvents: (runID, events, cursor) =>
    set((s) => {
      const log = s.sessionLogs[runID] ?? emptyLog
      return {
        sessionLogs: {
          ...s.sessionLogs,
          [runID]: { ...log, events: merge(log.events, events), cursor: Math.max(log.cursor, cursor ?? 0) },
        },
      }
    }),
  setSessionLogError: (runID, error) =>
    set((s) => ({
      sessionLogs: { ...s.sessionLogs, [runID]: { ...(s.sessionLogs[runID] ?? emptyLog), loading: false, error } },
    })),
  appendSessionEvent: (event) => {
    if (!get().sessionLogs[event.run_id] || !sessionEventTypes.includes(event.type)) return
    get().addSessionEvents(event.run_id, [event])
  },
})

export async function readSessionLog(store: RootStore, client: Api, run: { id: string; workspace_id: string }): Promise<void> {
  const state = store.getState()
  if (state.sessionLogs[run.id]?.loading) return
  state.beginSessionLog(run.id)
  let cursor = state.sessionLogs[run.id]?.cursor ?? 0
  try {
    for (;;) {
      const page = await client.workspaceTimeline({
        workspace_id: run.workspace_id,
        run_id: run.id,
        types: sessionEventTypes,
        after_seq: cursor,
        limit: pageSize,
      })
      store.getState().addSessionEvents(run.id, page.events, page.next_seq)
      cursor = page.next_seq
      if (!page.more) break
    }
    store.getState().setSessionLogError(run.id, null)
  } catch (err) {
    store.getState().setSessionLogError(run.id, message(err))
  }
}

export type Delivery = 'Queued' | 'Sent' | 'Not sent' | 'Delivery uncertain' | 'Denied' | 'Cancelled'

const deliveryWord: Record<RoomMessageState, Delivery> = {
  queued: 'Queued',
  sent: 'Sent',
  not_sent: 'Not sent',
  uncertain: 'Delivery uncertain',
  denied: 'Denied',
  cancelled: 'Cancelled',
}

export interface WorkEntry {
  id: string
  label: string
  status: 'running' | 'done' | 'failed'
  at: string
}

export type SessionRow =
  | { kind: 'user'; id: string; at: string; authorID: string; body: string; delivery: Delivery; deliverAfter?: string; failure?: string }
  | { kind: 'note'; id: string; at: string; authorID: string; body: string }
  | { kind: 'work'; id: string; at: string; summary: string; entries: WorkEntry[] }
  | {
      kind: 'request'
      id: string
      at: string
      answer: 'terminal' | 'reply'
      title: string
      body?: string
      authorID?: string
      reply?: { authorID: string; body: string }
    }
  | { kind: 'event'; id: string; at: string; text: string }
  | { kind: 'finished'; id: string; at: string; text: string; tone: Tone }

export interface SessionSources {
  run: RunRecord
  events: Event[]
  room: RoomMessage[]
  memberName: (id: string) => string
}

type Item =
  | { at: string; order: number; row: SessionRow }
  | { at: string; order: number; tool: AgentPayload; id: string }

const categories: [RegExp, string, string, string][] = [
  [/^(bash)$/, 'Ran', 'command', 'commands'],
  [/^(read)$/, 'Read', 'file', 'files'],
  [/^(edit|multiedit|notebookedit|write)$/, 'Edited', 'file', 'files'],
  [/^(grep|glob|websearch)$/, 'Ran', 'search', 'searches'],
  [/^(webfetch)$/, 'Fetched', 'page', 'pages'],
  [/^(todowrite)$/, 'Updated', 'plan', 'plans'],
]

export function workSummary(tools: AgentPayload[]): string {
  const counts = new Map<string, { verb: string; one: string; many: string; n: number }>()
  for (const tool of tools) {
    const name = (tool.tool ?? '').toLowerCase()
    const match = tool.kind === 'subagent'
      ? (['', 'Delegated', 'task', 'tasks'] as const)
      : categories.find(([pattern]) => pattern.test(name)) ?? ['', 'Used', 'tool', 'tools']
    const key = `${match[1]} ${match[3]}`
    const entry = counts.get(key) ?? { verb: match[1], one: match[2], many: match[3], n: 0 }
    entry.n++
    counts.set(key, entry)
  }
  const parts = [...counts.values()].map(({ verb, one, many, n }, index) =>
    `${index === 0 ? verb : verb.toLowerCase()} ${n} ${n === 1 ? one : many}`)
  return parts.length > 1 ? `${parts.slice(0, -1).join(', ')} and ${parts.at(-1)}` : parts[0] ?? ''
}

function workRow(calls: { id: string; at: string; tool: AgentPayload }[], results: Map<string, AgentPayload>): SessionRow {
  const entries = calls.map(({ id, at, tool }): WorkEntry => {
    const result = tool.tool_use_id ? results.get(tool.tool_use_id) : undefined
    const [present, past] = toolTenses(tool)
    const target = tool.detail || tool.tool || ''
    const status = result ? (result.is_error ? 'failed' : 'done') : 'running'
    return { id, at, status, label: `${status === 'running' ? present : past} ${target}`.trimEnd() }
  })
  return { kind: 'work', id: `work:${calls[0]!.id}`, at: calls[0]!.at, summary: workSummary(calls.map((c) => c.tool)), entries }
}

interface TimelinePayload {
  kind?: string
  message?: string
  outcome?: string
  summary?: string
}

function timelineRow(event: Event, name: (id: string) => string): SessionRow | null {
  const p = (event.payload ?? {}) as TimelinePayload
  const who = event.actor_id ? name(event.actor_id) : 'The agent'
  const base = { id: event.id, at: event.time }
  switch (p.kind) {
    case 'steer':
      return { ...base, kind: 'user', authorID: event.actor_id, body: p.message ?? '', delivery: 'Sent' }
    case 'pause':
      return { ...base, kind: 'event', text: `Paused by ${who}` }
    case 'resume':
      return { ...base, kind: 'event', text: `Resumed by ${who}` }
    case 'kill':
      return { ...base, kind: 'event', text: `Stopped by ${who}` }
    case 'handoff':
      return { ...base, kind: 'event', text: `${who} handed the run to ${name(p.message ?? '')}` }
    case 'co-author':
      return { ...base, kind: 'event', text: `${who} steered the run and is now a co-author` }
    case 'report':
      return { ...base, kind: 'event', text: ['Agent report', p.outcome, p.summary].filter(Boolean).join(': ') }
    default:
      return null
  }
}

const finishedText: Partial<Record<string, string>> = {
  completed: 'Finished',
  merged: 'Merged',
  abandoned: 'Closed without merging',
  failed: 'Failed',
  interrupted: 'Interrupted',
}

function statusRow(event: Event): SessionRow | null {
  const p = (event.payload ?? {}) as { to?: string; reason?: string }
  const text = finishedText[p.to ?? '']
  if (!text) return null
  return {
    kind: 'finished',
    id: event.id,
    at: event.time,
    text: p.reason ? `${text}: ${p.reason}` : text,
    tone: p.to === 'failed' || p.to === 'interrupted' ? 'failed' : 'done',
  }
}

const requestTitle: Record<RunInputRequest['kind'], string> = {
  permission: 'The agent asks for permission',
  question: 'The agent asks a question',
  form: 'The agent asks you to fill in a form',
  extension_ui: 'The agent opened a dialog',
}

/** Room steers are delivered through the same inject that records a timeline
 * steer, so a timeline steer matching a room message is the same message. */
function sameSteer(row: SessionRow, steer: RoomMessage): boolean {
  return row.kind === 'user' && steer.body.trim() === row.body.trim() &&
    steer.actor_id === row.authorID && Math.abs(Date.parse(steer.created_at) - Date.parse(row.at)) < 120_000
}

export function rowsForRun({ run, events, room, memberName }: SessionSources): SessionRow[] {
  const items: Item[] = [{
    at: run.created_at,
    order: -1,
    row: { kind: 'event', id: `start:${run.id}`, at: run.created_at, text: 'Run started' },
  }]
  const steers = room.filter((m) => m.kind === 'steer_request')
  const results = new Map<string, AgentPayload>()
  let finished = false

  events.forEach((event, order) => {
    if (event.type === 'run.agent') {
      const tool = (event.payload ?? {}) as AgentPayload
      if (tool.kind === 'tool_result' && tool.tool_use_id) results.set(tool.tool_use_id, tool)
      if (tool.kind === 'tool_call' || tool.kind === 'subagent') items.push({ at: event.time, order, tool, id: event.id })
      if (tool.kind === 'pause') {
        items.push({ at: event.time, order, row: { kind: 'event', id: event.id, at: event.time, text: tool.detail ? `Paused for review: ${tool.detail}` : 'Paused for review' } })
      }
      return
    }
    const row = event.type === 'run.status' ? statusRow(event) : timelineRow(event, memberName)
    if (!row || steers.some((steer) => sameSteer(row, steer))) return
    if (row.kind === 'finished') finished = true
    items.push({ at: event.time, order, row })
  })

  const replies = new Map(room.filter((m) => m.kind === 'reply' && m.correlation_id).map((m) => [m.correlation_id!, m]))
  room.forEach((m, index) => {
    const base = { id: m.id, at: m.created_at }
    const order = events.length + index
    if (m.kind === 'steer_request') {
      items.push({ at: m.created_at, order, row: {
        ...base, kind: 'user', authorID: m.actor_id, body: m.body, delivery: deliveryWord[m.state],
        deliverAfter: m.state === 'queued' ? m.deliver_after : undefined, failure: m.failure?.message,
      } })
    } else if (m.kind === 'comment') {
      items.push({ at: m.created_at, order, row: { ...base, kind: 'note', authorID: m.actor_id, body: m.body } })
    } else if (m.kind === 'question') {
      const reply = replies.get(m.id)
      items.push({ at: m.created_at, order, row: {
        ...base, kind: 'request', answer: 'reply', authorID: m.actor_id, title: `${memberName(m.actor_id)} asked`, body: m.body,
        reply: reply && { authorID: reply.actor_id, body: reply.body },
      } })
    } else if (m.kind === 'system') {
      items.push({ at: m.created_at, order, row: { ...base, kind: 'event', text: m.body } })
    }
  })

  if (!finished && isTerminal(run.status)) {
    const at = run.finished_at ?? run.stateChangedAt
    items.push({ at, order: Number.MAX_SAFE_INTEGER, row: {
      kind: 'finished', id: `finished:${run.id}`, at,
      text: run.reason ? `${finishedText[run.status]}: ${run.reason}` : finishedText[run.status] ?? 'Finished',
      tone: run.status === 'failed' || run.status === 'interrupted' ? 'failed' : 'done',
    } })
  }

  items.sort((a, b) => Date.parse(a.at) - Date.parse(b.at) || a.order - b.order)

  const rows: SessionRow[] = []
  let calls: { id: string; at: string; tool: AgentPayload }[] = []
  const flush = () => {
    if (calls.length) rows.push(workRow(calls, results))
    calls = []
  }
  for (const item of items) {
    if ('tool' in item) calls.push(item)
    else {
      flush()
      rows.push(item.row)
    }
  }
  flush()

  for (const request of run.pending_inputs ?? []) {
    rows.push({
      kind: 'request',
      id: `input:${request.id}`,
      at: run.stateChangedAt,
      answer: 'terminal',
      title: requestTitle[request.kind],
      body: 'Answer in the terminal',
    })
  }
  return rows
}
