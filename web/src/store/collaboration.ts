import type {
  EvidencePacket,
  RoomMessage,
  RoomStatusResult,
} from '@/lib/types'
import type { ControlMetadata } from '@/routes/terminal/attach'
import type { SliceCreator } from '@/store/slice'

export interface PaginationState {
  initialized: boolean
  exhausted: boolean
}

export interface FailedNote {
  text: string
  key: string
  error: string
  unanswered: boolean
}

export interface CollaborationSlice {
  roomMessages: Record<string, RoomMessage[]>
  roomNextBefore: Record<string, string | undefined>
  roomPagination: Record<string, PaginationState>
  roomStatus: Record<string, RoomStatusResult | undefined>
  /** Exact acknowledged state: taking then releasing can restore the same generation. */
  roomStatusControl: Record<string, ControlMetadata | undefined>
  roomStatusError: Record<string, string | undefined>
  roomLoading: Record<string, boolean | undefined>
  /** Errors reading room history; presence and action failures live separately. */
  roomError: Record<string, string | undefined>
  roomActionError: Record<string, string | undefined>
  noteDrafts: Record<string, string | undefined>
  /** Per run, the key of each note text sent and not yet saved, so a resend never adds a second copy. */
  noteKeys: Record<string, Record<string, string> | undefined>
  failedNotes: Record<string, FailedNote[] | undefined>
  evidencePackets: Record<string, EvidencePacket[]>
  evidenceNextBefore: Record<string, string | undefined>
  evidencePagination: Record<string, PaginationState>
  evidenceLoading: Record<string, boolean | undefined>
  evidenceError: Record<string, string | undefined>
  selectedEvidence: Record<string, string | undefined>
  initializeRoomPagination: (runID: string) => void
  setRoomLoading: (runID: string, loading: boolean) => void
  setRoomError: (runID: string, error?: string) => void
  setRoomActionError: (runID: string, error?: string) => void
  setRoomPage: (runID: string, messages: RoomMessage[], nextBefore?: string, append?: boolean) => void
  upsertRoomMessage: (message: RoomMessage) => void
  dropRoomMessage: (runID: string, id: string) => void
  setNoteDraft: (runID: string, text: string) => void
  noteSending: (runID: string, text: string, key: string) => void
  /** A failure for a note whose saved copy is already here is ignored. */
  noteFailed: (runID: string, note: FailedNote) => void
  /** A refusal that came after the deadline replaces that note's failure. */
  noteRefused: (runID: string, key: string, error: string) => void
  dismissFailedNote: (runID: string, key: string) => void
  resetNotes: () => void
  setRoomStatus: (runID: string, status: RoomStatusResult, control: ControlMetadata | undefined) => void
  setRoomStatusError: (runID: string, error?: string) => void
  initializeEvidencePagination: (runID: string) => void
  setEvidenceLoading: (runID: string, loading: boolean) => void
  setEvidenceError: (runID: string, error?: string) => void
  setEvidencePage: (runID: string, packets: EvidencePacket[], nextBefore?: string, append?: boolean) => void
  upsertEvidencePacket: (packet: EvidencePacket) => void
  selectEvidence: (runID: string, packetID?: string) => void
}

const RFC3339_NANO = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(\.\d+)?(Z|[+-]\d{2}:\d{2})$/
const NANOS_PER_SECOND = 1_000_000_000n

/**
 * RFC3339Nano drops trailing fractional zeroes, so wire strings are not
 * lexically ordered (`.1Z` is older than `.11Z`). Keep the full fractional
 * precision while comparing instants.
 */
function instant(value: string): bigint {
  const match = RFC3339_NANO.exec(value)
  if (!match) {
    const milliseconds = Date.parse(value)
    return Number.isNaN(milliseconds) ? 0n : BigInt(milliseconds) * 1_000_000n
  }
  const [, year, month, day, hour, minute, second, fraction, zone] = match
  let secondsSinceEpoch = BigInt(
    Math.trunc(Date.UTC(Number(year), Number(month) - 1, Number(day), Number(hour), Number(minute), Number(second)) / 1000),
  )
  if (zone !== 'Z') {
    const sign = zone[0] === '+' ? 1n : -1n
    const offsetMinutes = BigInt(Number(zone.slice(1, 3)) * 60 + Number(zone.slice(4, 6)))
    secondsSinceEpoch -= sign * offsetMinutes * 60n
  }
  const nanos = BigInt(((fraction?.slice(1) ?? '') + '000000000').slice(0, 9))
  return secondsSinceEpoch * NANOS_PER_SECOND + nanos
}

export function compareInstants(left: string, right: string): number {
  const a = instant(left)
  const b = instant(right)
  return a < b ? -1 : a > b ? 1 : 0
}


export const byCreated = <T extends { created_at: string; id: string }>(items: T[]) =>
  [...items].sort((a, b) => compareInstants(a.created_at, b.created_at) || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0))

function mergeByID<T extends { id: string; updated_at: string }>(current: T[], incoming: T[]): T[] {
  const merged = new Map(current.map((item) => [item.id, item]))
  for (const item of incoming) {
    const prior = merged.get(item.id)
    if (!prior || compareInstants(item.updated_at, prior.updated_at) >= 0) merged.set(item.id, item)
  }
  return [...merged.values()]
}

/** A provisional message is identified by its idempotency key until the server's copy replaces it. */
export const isProvisional = (message: RoomMessage) => message.id === message.idempotency_key

/**
 * A page lists newest first, and its order wins for the messages it holds; an event keeps a held message where it is.
 * The stable sort then keeps the server's insertion order among same-instant messages.
 */
function mergeRoomMessages(current: RoomMessage[], incoming: RoomMessage[], from: 'event' | 'page' | 'older page'): RoomMessage[] {
  const saved = new Set([...current, ...incoming].filter((m) => !isProvisional(m)).map((m) => m.idempotency_key))
  const pending = (m: RoomMessage) => isProvisional(m) && saved.has(m.id)
  const kept = current.filter((m) => !pending(m))
  const page = incoming.filter((m) => !pending(m)).reverse()
  const merged = new Map(mergeByID(kept, page).map((m) => [m.id, m]))
  const listed = new Set(page.map((m) => m.id))
  const rest = from === 'event' ? kept : kept.filter((m) => !listed.has(m.id))
  const order = new Set((from === 'older page' ? [...page, ...rest] : [...rest, ...page]).map((m) => m.id))
  return [...order].map((id) => merged.get(id)!).sort((a, b) => compareInstants(a.created_at, b.created_at))
}

const isSaved = (messages: RoomMessage[], key: string) => messages.some((m) => !isProvisional(m) && m.idempotency_key === key)

function settleNotes(state: CollaborationSlice, runID: string, messages: RoomMessage[]): Partial<CollaborationSlice> {
  const keys = state.noteKeys[runID] ?? {}
  const failed = state.failedNotes[runID] ?? []
  if (!failed.length && !Object.keys(keys).length) return {}
  const draft = state.noteDrafts[runID]?.trim()
  const saved = failed.filter((n) => isSaved(messages, n.key))
  return {
    noteKeys: { ...state.noteKeys, [runID]: Object.fromEntries(Object.entries(keys).filter(([, key]) => !isSaved(messages, key))) },
    failedNotes: { ...state.failedNotes, [runID]: failed.filter((n) => !saved.includes(n)) },
    ...(saved.some((n) => n.text === draft) ? { noteDrafts: { ...state.noteDrafts, [runID]: '' } } : {}),
  }
}

function pagePagination(
  previous: PaginationState | undefined,
  currentCursor: string | undefined,
  nextBefore: string | undefined,
  append: boolean,
): { pagination: PaginationState; cursor: string | undefined } {
  if (append) {
    if (previous?.initialized && previous.exhausted) {
      return { pagination: previous, cursor: undefined }
    }
    return {
      pagination: { initialized: true, exhausted: nextBefore === undefined },
      cursor: nextBefore,
    }
  }
  if (!previous || !previous.initialized) {
    return {
      pagination: { initialized: true, exhausted: nextBefore === undefined },
      cursor: nextBefore,
    }
  }
  if (previous.exhausted) {
    // Refreshes read the newest page again. They must not turn an exhausted
    // history back into a paginatable one just because newer messages made
    // that page return a cursor.
    return { pagination: previous, cursor: undefined }
  }
  // The cursor belongs to the oldest page already cached. A newest-page
  // refresh does not own it and must not move it forward or backward.
  return { pagination: previous, cursor: currentCursor }
}

export const createCollaborationSlice: SliceCreator<CollaborationSlice> = (set) => ({
  roomMessages: {},
  roomNextBefore: {},
  roomPagination: {},
  roomStatus: {},
  roomStatusControl: {},
  roomStatusError: {},
  roomLoading: {},
  roomError: {},
  roomActionError: {},
  noteDrafts: {},
  noteKeys: {},
  failedNotes: {},
  evidencePackets: {},
  evidenceNextBefore: {},
  evidencePagination: {},
  evidenceLoading: {},
  evidenceError: {},
  selectedEvidence: {},
  initializeRoomPagination: (runID) =>
    set((state) => ({
      roomMessages: state.roomMessages[runID]
        ? state.roomMessages
        : { ...state.roomMessages, [runID]: [] },
      roomPagination: state.roomPagination[runID]
        ? state.roomPagination
        : {
            ...state.roomPagination,
            [runID]: { initialized: false, exhausted: false },
          },
    })),
  setRoomLoading: (runID, loading) =>
    set((state) => ({ roomLoading: { ...state.roomLoading, [runID]: loading } })),
  setRoomError: (runID, error) =>
    set((state) => ({ roomError: { ...state.roomError, [runID]: error } })),
  setRoomActionError: (runID, error) =>
    set((state) => ({ roomActionError: { ...state.roomActionError, [runID]: error } })),
  setRoomStatusError: (runID, error) =>
    set((state) => ({ roomStatusError: { ...state.roomStatusError, [runID]: error } })),
  setRoomPage: (runID, messages, nextBefore, append = false) =>
    set((state) => {
      const page = pagePagination(
        state.roomPagination[runID],
        state.roomNextBefore[runID],
        nextBefore,
        append,
      )
      const merged = mergeRoomMessages(state.roomMessages[runID] ?? [], messages, append ? 'older page' : 'page')
      return {
        ...settleNotes(state, runID, merged),
        roomMessages: { ...state.roomMessages, [runID]: merged },
        roomNextBefore: {
          ...state.roomNextBefore,
          [runID]: page.cursor,
        },
        roomPagination: {
          ...state.roomPagination,
          [runID]: page.pagination,
        },
      }
    }),
  upsertRoomMessage: (message) =>
    set((state) => {
      const merged = mergeRoomMessages(state.roomMessages[message.run_id] ?? [], [message], 'event')
      return {
        ...settleNotes(state, message.run_id, merged),
        roomMessages: { ...state.roomMessages, [message.run_id]: merged },
      }
    }),
  dropRoomMessage: (runID, id) =>
    set((state) => ({
      roomMessages: {
        ...state.roomMessages,
        [runID]: (state.roomMessages[runID] ?? []).filter((m) => m.id !== id),
      },
    })),
  setNoteDraft: (runID, text) =>
    set((state) => ({ noteDrafts: { ...state.noteDrafts, [runID]: text } })),
  noteSending: (runID, text, key) =>
    set((state) => ({
      noteKeys: { ...state.noteKeys, [runID]: { ...state.noteKeys[runID], [text]: key } },
      failedNotes: { ...state.failedNotes, [runID]: (state.failedNotes[runID] ?? []).filter((n) => n.text !== text) },
      noteDrafts: state.noteDrafts[runID]?.trim() === text ? { ...state.noteDrafts, [runID]: '' } : state.noteDrafts,
    })),
  noteFailed: (runID, note) =>
    set((state) => {
      if (isSaved(state.roomMessages[runID] ?? [], note.key)) return {}
      return {
        failedNotes: { ...state.failedNotes, [runID]: [...(state.failedNotes[runID] ?? []).filter((n) => n.key !== note.key), note] },
        noteDrafts: state.noteDrafts[runID]?.trim() ? state.noteDrafts : { ...state.noteDrafts, [runID]: note.text },
      }
    }),
  noteRefused: (runID, key, error) =>
    set((state) => ({
      failedNotes: {
        ...state.failedNotes,
        [runID]: (state.failedNotes[runID] ?? []).map((n) => (n.key === key ? { ...n, error, unanswered: false } : n)),
      },
    })),
  dismissFailedNote: (runID, key) =>
    set((state) => ({ failedNotes: { ...state.failedNotes, [runID]: (state.failedNotes[runID] ?? []).filter((n) => n.key !== key) } })),
  resetNotes: () =>
    set((state) => ({
      noteDrafts: {},
      noteKeys: {},
      failedNotes: {},
      roomMessages: Object.fromEntries(
        Object.entries(state.roomMessages).map(([runID, messages]) => [runID, messages.filter((m) => !isProvisional(m))]),
      ),
    })),
  setRoomStatus: (runID, status, control) =>
    set((state) => ({
      roomStatus: { ...state.roomStatus, [runID]: status },
      roomStatusControl: { ...state.roomStatusControl, [runID]: control },
    })),
  initializeEvidencePagination: (runID) =>
    set((state) => ({
      evidencePackets: state.evidencePackets[runID]
        ? state.evidencePackets
        : { ...state.evidencePackets, [runID]: [] },
      evidencePagination: state.evidencePagination[runID]
        ? state.evidencePagination
        : {
            ...state.evidencePagination,
            [runID]: { initialized: false, exhausted: false },
          },
    })),
  setEvidenceLoading: (runID, loading) =>
    set((state) => ({ evidenceLoading: { ...state.evidenceLoading, [runID]: loading } })),
  setEvidenceError: (runID, error) =>
    set((state) => ({ evidenceError: { ...state.evidenceError, [runID]: error } })),
  setEvidencePage: (runID, packets, nextBefore, append = false) =>
    set((state) => {
      const page = pagePagination(
        state.evidencePagination[runID],
        state.evidenceNextBefore[runID],
        nextBefore,
        append,
      )
      return {
        evidencePackets: {
          ...state.evidencePackets,
          [runID]: byCreated(
            mergeByID(state.evidencePackets[runID] ?? [], packets),
          ),
        },
        evidenceNextBefore: {
          ...state.evidenceNextBefore,
          [runID]: page.cursor,
        },
        evidencePagination: {
          ...state.evidencePagination,
          [runID]: page.pagination,
        },
      }
    }),
  upsertEvidencePacket: (packet) =>
    set((state) => ({
      evidencePackets: {
        ...state.evidencePackets,
        [packet.run_id]: byCreated(
          mergeByID(state.evidencePackets[packet.run_id] ?? [], [packet]),
        ),
      },
    })),
  selectEvidence: (runID, packetID) =>
    set((state) => ({
      selectedEvidence: { ...state.selectedEvidence, [runID]: packetID },
    })),
})

export function unansweredQuestions(messages: RoomMessage[]): RoomMessage[] {
  const answered = new Set(
    messages
      .filter((message) => message.kind === 'reply' && message.correlation_id)
      .map((message) => message.correlation_id as string),
  )
  return messages.filter(
    (message) =>
      message.kind === 'question' &&
      !answered.has(message.id) &&
      message.state !== 'denied' &&
      message.state !== 'cancelled',
  )
}

export function queuedSteers(messages: RoomMessage[]): RoomMessage[] {
  return messages.filter(
    (message) => message.kind === 'steer_request' && message.state === 'queued',
  )
}

export const isNote = (message: RoomMessage) => message.kind === 'comment' || message.kind === 'question'

export function repliesByQuestion(messages: RoomMessage[]): Map<string, RoomMessage[]> {
  const replies = new Map<string, RoomMessage[]>()
  for (const message of messages) {
    if (message.kind !== 'reply' || !message.correlation_id) continue
    replies.set(message.correlation_id, [...(replies.get(message.correlation_id) ?? []), message])
  }
  return replies
}
