import type { RoomMessage, RoomStatusResult } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

export interface PaginationState {
  initialized: boolean
  exhausted: boolean
}

export interface CollaborationSlice {
  roomMessages: Record<string, RoomMessage[]>
  roomNextBefore: Record<string, string | undefined>
  roomPagination: Record<string, PaginationState>
  roomStatus: Record<string, RoomStatusResult | undefined>
  roomLoading: Record<string, boolean | undefined>
  /** Errors reading room history or status; action failures live separately. */
  roomError: Record<string, string | undefined>
  roomActionError: Record<string, string | undefined>
  initializeRoomPagination: (runID: string) => void
  setRoomLoading: (runID: string, loading: boolean) => void
  setRoomError: (runID: string, error?: string) => void
  setRoomActionError: (runID: string, error?: string) => void
  setRoomPage: (runID: string, messages: RoomMessage[], nextBefore?: string, append?: boolean) => void
  upsertRoomMessage: (message: RoomMessage) => void
  setRoomStatus: (runID: string, status: RoomStatusResult) => void
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

function compareInstants(left: string, right: string): number {
  const a = instant(left)
  const b = instant(right)
  return a < b ? -1 : a > b ? 1 : 0
}


const byCreated = <T extends { created_at: string; id: string }>(items: T[]) =>
  [...items].sort((a, b) => compareInstants(a.created_at, b.created_at) || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0))

function mergeByID<T extends { id: string; updated_at: string }>(current: T[], incoming: T[]): T[] {
  const merged = new Map(current.map((item) => [item.id, item]))
  for (const item of incoming) {
    const prior = merged.get(item.id)
    if (!prior || compareInstants(item.updated_at, prior.updated_at) >= 0) merged.set(item.id, item)
  }
  return [...merged.values()]
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
  roomLoading: {},
  roomError: {},
  roomActionError: {},
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
  setRoomPage: (runID, messages, nextBefore, append = false) =>
    set((state) => {
      const page = pagePagination(
        state.roomPagination[runID],
        state.roomNextBefore[runID],
        nextBefore,
        append,
      )
      return {
        roomMessages: {
          ...state.roomMessages,
          [runID]: byCreated(
            mergeByID(state.roomMessages[runID] ?? [], messages),
          ),
        },
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
    set((state) => ({
      roomMessages: {
        ...state.roomMessages,
        [message.run_id]: byCreated(
          mergeByID(state.roomMessages[message.run_id] ?? [], [message]),
        ),
      },
    })),
  setRoomStatus: (runID, status) =>
    set((state) => ({ roomStatus: { ...state.roomStatus, [runID]: status } })),
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
