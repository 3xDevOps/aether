import type { ConnectionState } from '@/lib/stream'
import type { GatewayCapabilities, ServerInfo } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

/** Which hop is down when the app cannot reach its data.
 * `gateway`: the HTTP origin itself is gone - the `aether gui` process or
 * `aether dash` tunnel died - so nothing answers at all.
 * `server`: the gateway answers but its SSH backend cannot reach
 * aether-server (it reports 503 "server unreachable: ..."). */
export type UnreachableKind = 'gateway' | 'server'

export interface ServerSlice {
  info: ServerInfo | null
  /** What the gateway can do; null until fetched, and stays null on a
   * legacy server that does not serve the endpoint. */
  capabilities: GatewayCapabilities | null
  connection: ConnectionState
  /** Highest event sequence applied; the cursor a reconnect replays from. */
  lastSeq: number
  hydrated: boolean
  hydrationError: string | null
  /** The stream stopped for good - a dead token - and nothing retries. */
  streamDead: boolean
  /** Which hop failed on the last fetch, or null when reachable. */
  unreachable: UnreachableKind | null
  setInfo: (info: ServerInfo) => void
  setCapabilities: (capabilities: GatewayCapabilities | null) => void
  setConnection: (state: ConnectionState) => void
  noteSeq: (seq: number) => void
  /** Forgets the cursor after the server's event log restarted under us. */
  resetSeq: () => void
  setHydrated: (hydrated: boolean, error?: string | null) => void
  setStreamDead: () => void
  setUnreachable: (kind: UnreachableKind | null) => void
}

export const createServerSlice: SliceCreator<ServerSlice> = (set) => ({
  info: null,
  capabilities: null,
  connection: 'connecting',
  lastSeq: 0,
  hydrated: false,
  hydrationError: null,
  streamDead: false,
  unreachable: null,
  setInfo: (info) => set({ info }),
  setCapabilities: (capabilities) => set({ capabilities }),
  setConnection: (connection) => set({ connection }),
  noteSeq: (seq) =>
    set((s) => (seq > s.lastSeq ? { lastSeq: seq } : {})),
  resetSeq: () => set({ lastSeq: 0 }),
  setHydrated: (hydrated, error = null) =>
    set({ hydrated, hydrationError: error }),
  setStreamDead: () => set({ streamDead: true }),
  setUnreachable: (unreachable) => set({ unreachable }),
})
