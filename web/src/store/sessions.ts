import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import { inputTitle } from '@/lib/run-requests'
import { plainReason } from '@/lib/status'
import type { StreamState } from '@/lib/acp-stream'
import type { SessionFrame, SessionItem, SessionRequest, SessionState, SessionStreamAck } from '@/lib/session-types'
import type { Event, RoomMessage, RoomMessageState } from '@/lib/types'
import type { ControlMetadata } from '@/routes/terminal/attach'
import type { TakeoverSnapshot } from '@/routes/terminal/session'
import type { RootStore } from '@/store'
import { toolTenses, type AgentPayload } from '@/store/activity'
import { isTerminal, type RunRecord } from '@/store/runs'
import {
  appendItems,
  itemCount,
  prependItems,
  replaceItem,
  trimTurns,
  workSummary,
  type Delivery,
  type SessionRow,
  type Turn,
  type WorkItem,
} from '@/store/session-rows'
import type { SliceCreator } from '@/store/slice'

export type { Delivery, SessionRow }

export const sessionEventTypes = ['run.agent', 'workspace.timeline', 'run.status']

const residentEvents = 2000
const pageSize = 1000

export interface SessionLog {
  events: Event[]
  cursor: number
  loading: boolean
  error: string | null
}

export interface AcpSession {
  epoch: number
  seq: number
  oldestSeq: number
  more: boolean
  truncatedBefore: boolean
  historyGeneration: number
  turns: Turn[]
  state: SessionState | null
  pending: SessionRequest[]
  live: boolean
  stream: StreamState
  streamError?: string
  control?: ControlMetadata
  controlError?: string
  takeover?: TakeoverSnapshot
  takeoverError?: string
  olderLoading: boolean
  olderError?: string
  touched: number
}

export interface SessionsSlice {
  sessionLogs: Record<string, SessionLog>
  beginSessionLog: (runID: string) => void
  addSessionEvents: (runID: string, events: Event[], cursor?: number) => void
  setSessionLogError: (runID: string, error: string | null) => void
  appendSessionEvent: (event: Event) => void

  acpSessions: Record<string, AcpSession>
  expandedRows: Record<string, Record<string, true>>
  acpAck: (runID: string, ack: SessionStreamAck) => void
  acpFrames: (runID: string, frames: SessionFrame[]) => void
  acpOlder: (runID: string, frames: SessionFrame[], more: boolean, truncatedBefore?: boolean) => void
  acpOlderState: (runID: string, loading: boolean, error?: string) => void
  acpReplaceItem: (runID: string, item: SessionItem) => void
  acpStream: (runID: string, stream: StreamState, error?: string) => void
  acpControl: (runID: string, control: ControlMetadata | undefined, error?: string) => void
  acpTakeover: (runID: string, takeover: TakeoverSnapshot | undefined, error?: string) => void
  touchAcpSession: (runID: string, open: (runID: string) => boolean) => void
  toggleSessionRow: (runID: string, rowID: string) => void
}

export const residentSessions = 3
export const keptItems = 200

let touches = 0

const emptyLog: SessionLog = { events: [], cursor: 0, loading: false, error: null }

function merge(current: Event[], incoming: Event[]): Event[] {
  const seen = new Set(current.map((event) => event.seq))
  const fresh = incoming.filter((event) => !seen.has(event.seq))
  if (fresh.length === 0) return current
  return [...current, ...fresh].sort((a, b) => a.seq - b.seq).slice(-residentEvents)
}

const emptySession: AcpSession = {
  epoch: 0,
  seq: 0,
  oldestSeq: 0,
  more: false,
  truncatedBefore: false,
  historyGeneration: 0,
  turns: [],
  state: null,
  pending: [],
  live: false,
  stream: 'connecting',
  olderLoading: false,
  touched: 0,
}

function itemsOf(frames: SessionFrame[]): SessionItem[] {
  return frames.flatMap((frame) => (frame.item ? [frame.truncated ? { ...frame.item, truncated: true } : frame.item] : []))
}

function advance(session: AcpSession, items: SessionItem[]): Pick<AcpSession, 'state' | 'pending'> {
  let state = session.state
  let pending = session.pending
  for (const item of items) {
    switch (item.kind) {
      case 'request': {
        const request = item.request
        if (!request) break
        pending = pending.filter((open) => open.id !== request.id)
        if (request.status === 'pending') pending = [...pending, request]
        break
      }
      case 'turn_start':
      case 'turn_end':
        if (state) state = { ...state, turn_in_flight: item.kind === 'turn_start' }
        if (item.kind === 'turn_end') pending = []
        break
      case 'mode_change':
        if (state) state = { ...state, mode: item.mode }
        break
      case 'config_options':
        if (state) state = { ...state, config_options: item.config_options }
        break
      case 'commands':
        if (state) state = { ...state, commands: item.commands }
        break
      case 'auth_status':
        if (state) state = { ...state, auth: item.auth }
        break
    }
  }
  return { state, pending }
}

function patchSession(s: SessionsSlice, runID: string, patch: Partial<AcpSession>): Pick<SessionsSlice, 'acpSessions'> {
  return { acpSessions: { ...s.acpSessions, [runID]: { ...(s.acpSessions[runID] ?? emptySession), ...patch } } }
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

  acpSessions: {},
  expandedRows: {},
  acpAck: (runID, ack) =>
    set((s) => {
      const held = s.acpSessions[runID] ?? emptySession
      const restart = ack.epoch !== held.epoch || ack.oldest_seq !== undefined
      const base = restart
        ? { turns: [], seq: 0, oldestSeq: 0, more: (ack.oldest_seq ?? 0) > 1, historyGeneration: held.historyGeneration + 1, olderLoading: false, olderError: undefined }
        : {}
      return patchSession(s, runID, { ...base, truncatedBefore: ack.truncated_before ?? (ack.epoch === held.epoch && held.truncatedBefore), epoch: ack.epoch, live: ack.live, state: ack.state ?? null, pending: ack.state?.pending ?? [] })
    }),
  acpFrames: (runID, frames) =>
    set((s) => {
      let session = s.acpSessions[runID] ?? emptySession
      let batch: SessionItem[] = []
      const flush = () => {
        if (batch.length === 0) return
        session = {
          ...session,
          ...advance(session, batch),
          turns: appendItems(session.turns, batch),
          seq: batch.at(-1)!.seq,
          oldestSeq: session.oldestSeq || batch[0]!.seq,
        }
        batch = []
      }
      for (const frame of frames) {
        if (frame.reset) {
          flush()
          const sameEpoch = frame.epoch === session.epoch
          session = {
            ...session, turns: [], seq: 0, oldestSeq: 0,
            more: sameEpoch && session.more,
            truncatedBefore: sameEpoch && session.truncatedBefore,
            historyGeneration: session.historyGeneration + 1,
            olderLoading: false, olderError: undefined,
            pending: sameEpoch ? session.pending : [],
            epoch: frame.epoch ?? session.epoch + 1,
          }
          continue
        }
        const [item] = itemsOf([frame])
        if (item && item.seq > (batch.at(-1)?.seq ?? session.seq)) batch.push(item)
      }
      flush()
      return { acpSessions: { ...s.acpSessions, [runID]: session } }
    }),
  acpOlder: (runID, frames, more, truncatedBefore = false) =>
    set((s) => {
      const session = s.acpSessions[runID]
      if (!session) return {}
      const older = itemsOf(frames).filter((item) => !session.oldestSeq || item.seq < session.oldestSeq)
      return patchSession(s, runID, {
        turns: prependItems(session.turns, older),
        oldestSeq: older[0]?.seq ?? session.oldestSeq,
        more: more && older.length > 0,
        truncatedBefore: session.truncatedBefore || truncatedBefore,
        olderLoading: false,
        olderError: undefined,
      })
    }),
  acpOlderState: (runID, loading, error) => set((s) => patchSession(s, runID, { olderLoading: loading, olderError: error })),
  acpReplaceItem: (runID, item) =>
    set((s) => {
      const session = s.acpSessions[runID]
      return session ? patchSession(s, runID, { turns: replaceItem(session.turns, item) }) : {}
    }),
  acpStream: (runID, stream, error) => set((s) => patchSession(s, runID, { stream, streamError: error })),
  acpControl: (runID, control, error) => set((s) => patchSession(s, runID, { control, controlError: error })),
  acpTakeover: (runID, takeover, error) => set((s) => patchSession(s, runID, { takeover, takeoverError: error })),
  touchAcpSession: (runID, open) =>
    set((s) => {
      const sessions = { ...s.acpSessions, [runID]: { ...(s.acpSessions[runID] ?? emptySession), touched: ++touches } }
      const idle = Object.entries(sessions)
        .filter(([id]) => id !== runID && !open(id))
        .sort(([, a], [, b]) => b.touched - a.touched)
      const room = Math.max(0, residentSessions - 1 - Object.keys(sessions).filter((id) => id !== runID && open(id)).length)
      for (const [id, session] of idle.slice(room)) {
        if (itemCount(session.turns) <= keptItems) continue
        const turns = trimTurns(session.turns, keptItems)
        sessions[id] = { ...session, turns, oldestSeq: turns[0]?.items[0]?.seq ?? session.oldestSeq, more: true }
      }
      return { acpSessions: sessions }
    }),
  toggleSessionRow: (runID, rowID) =>
    set((s) => {
      const rows = { ...s.expandedRows[runID] }
      if (rows[rowID]) delete rows[rowID]
      else rows[rowID] = true
      return { expandedRows: { ...s.expandedRows, [runID]: rows } }
    }),
})

export async function readSessionLog(store: RootStore, client: Api, run: { id: string; workspace_id: string }): Promise<void> {
  const state = store.getState()
  if (state.sessionLogs[run.id]?.loading) return
  state.beginSessionLog(run.id)
  const epoch = state.terminalCacheEpoch
  const query = { workspace_id: run.workspace_id, run_id: run.id, types: sessionEventTypes, limit: pageSize }
  let cursor = state.sessionLogs[run.id]?.cursor ?? 0
  try {
    if (cursor === 0) {
      let before: number | undefined
      for (let read = 0; read < residentEvents;) {
        const page = await client.workspaceTimeline({ ...query, newest: true, before_seq: before })
        if (store.getState().terminalCacheEpoch !== epoch) return
        store.getState().addSessionEvents(run.id, page.events, page.next_seq)
        read += page.events.length
        before = page.older_seq
        if (!page.more) break
      }
    } else {
      for (;;) {
        const page = await client.workspaceTimeline({ ...query, after_seq: cursor })
        if (store.getState().terminalCacheEpoch !== epoch) return
        store.getState().addSessionEvents(run.id, page.events, page.next_seq)
        cursor = page.next_seq
        if (!page.more) break
      }
    }
    store.getState().setSessionLogError(run.id, null)
  } catch (err) {
    if (store.getState().terminalCacheEpoch === epoch) store.getState().setSessionLogError(run.id, message(err))
  }
}

const deliveryWord: Record<RoomMessageState, Delivery> = {
  queued: 'Queued',
  sent: 'Sent',
  not_sent: 'Not sent',
  uncertain: 'Delivery uncertain',
  denied: 'Denied',
  cancelled: 'Cancelled',
}

export function deliveryOf(m: RoomMessage): Pick<Extract<SessionRow, { kind: 'user' }>, 'delivery' | 'deliverAfter' | 'failure'> {
  return {
    delivery: m.state === 'sent' && m.agent_delivery === 'queued' ? 'Queued' : deliveryWord[m.state],
    deliverAfter: m.state === 'queued' ? m.deliver_after : undefined,
    failure: m.failure?.message,
  }
}

export function roomImages(m: Pick<RoomMessage, 'id' | 'attachments'>): Extract<SessionRow, { kind: 'user' }>['images'] {
  return m.attachments?.length ? m.attachments.map((_, index) => ({ messageID: m.id, index })) : undefined
}

export interface SessionSources {
  run: RunRecord
  events: Event[]
  room: RoomMessage[]
  memberName: (id: string) => string
  paused: boolean
}

type Item =
  | { at: string; order: number; row: SessionRow }
  | { at: string; order: number; tool: AgentPayload; id: string }

function workRow(calls: { id: string; at: string; tool: AgentPayload }[], results: Map<string, AgentPayload>): SessionRow {
  const tools = calls.map(({ tool }) => (tool.kind === 'subagent' ? 'subagent' : tool.tool ?? ''))
  const entries = calls.map(({ id, at, tool }, index): WorkItem => {
    const result = tool.tool_use_id ? results.get(tool.tool_use_id) : undefined
    const [present, past] = toolTenses(tool)
    const target = tool.detail || tool.tool || ''
    const status = result ? (result.is_error ? 'failed' : 'done') : 'running'
    return { id, at, status, tool: tools[index]!, label: `${status === 'running' ? present : past} ${target}`.trimEnd() }
  })
  return { kind: 'work', id: `work:${calls[0]!.id}`, at: calls[0]!.at, summary: workSummary(tools), entries }
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
      return { ...base, kind: 'event', text: `${who} messaged the agent and is now a co-author` }
    case 'report':
      return { ...base, kind: 'event', text: ['Agent report', p.outcome, p.summary].filter(Boolean).join(': '), report: true }
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
    text: p.reason ? `${text}: ${plainReason(p.reason)}` : text,
    tone: p.to === 'failed' || p.to === 'interrupted' ? 'failed' : 'done',
  }
}

/** Room steers are delivered through the same inject that records a timeline
 * steer, so a timeline steer matching a room message is the same message. */
function sameSteer(row: SessionRow, steer: RoomMessage): boolean {
  return row.kind === 'user' && steer.body.trim() === row.body.trim() &&
    steer.actor_id === row.authorID && Math.abs(Date.parse(steer.created_at) - Date.parse(row.at)) < 120_000
}

function liveRows(run: RunRecord, paused: boolean): SessionRow[] {
  if (run.status !== 'running' || paused || run.pending_inputs?.length) return []
  const at = run.activity?.at ?? run.stateChangedAt
  const label = run.activity?.running ? `${run.activity.verb} ${run.activity.target}`.trimEnd() : 'Working'
  return [
    { kind: 'live', id: 'live', at, label },
    { kind: 'terminal-note', id: 'terminal-note', at },
  ]
}

export function rowsForRun({ run, events, room, memberName, paused }: SessionSources): SessionRow[] {
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
        ...base, kind: 'user', authorID: m.actor_id, body: m.body, images: roomImages(m), ...deliveryOf(m),
      } })
    } else if (m.kind === 'comment') {
      items.push({ at: m.created_at, order, row: { ...base, kind: 'note', authorID: m.actor_id, body: m.body } })
    } else if (m.kind === 'question' && m.actor_id === run.member_id) {
      items.push({ at: m.created_at, order, row: { ...base, kind: 'note', authorID: m.actor_id, body: m.body, question: true } })
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
      text: run.reason ? `${finishedText[run.status]}: ${plainReason(run.reason)}` : finishedText[run.status] ?? 'Finished',
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
      answer: 'input',
      title: inputTitle[request.kind],
    })
  }
  return [...rows, ...liveRows(run, paused)]
}
