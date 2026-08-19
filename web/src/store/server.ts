import type { ConnectionState } from '@/lib/stream'
import type { ServerInfo } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

export interface ServerSlice {
  info: ServerInfo | null
  connection: ConnectionState
  /** Highest event sequence applied; the cursor a reconnect replays from. */
  lastSeq: number
  hydrated: boolean
  hydrationError: string | null
  /** The stream stopped for good - a dead token - and nothing retries. */
  streamDead: boolean
  setInfo: (info: ServerInfo) => void
  setConnection: (state: ConnectionState) => void
  noteSeq: (seq: number) => void
  /** Forgets the cursor after the server's event log restarted under us. */
  resetSeq: () => void
  setHydrated: (hydrated: boolean, error?: string | null) => void
  setStreamDead: () => void
}

export const createServerSlice: SliceCreator<ServerSlice> = (set) => ({
  info: null,
  connection: 'connecting',
  lastSeq: 0,
  hydrated: false,
  hydrationError: null,
  streamDead: false,
  setInfo: (info) => set({ info }),
  setConnection: (connection) => set({ connection }),
  noteSeq: (seq) =>
    set((s) => (seq > s.lastSeq ? { lastSeq: seq } : {})),
  resetSeq: () => set({ lastSeq: 0 }),
  setHydrated: (hydrated, error = null) =>
    set({ hydrated, hydrationError: error }),
  setStreamDead: () => set({ streamDead: true }),
})
