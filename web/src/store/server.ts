import { bareVersion } from '@/lib/format'
import type { ConnectionState } from '@/lib/stream'
import type {
  GatewayCapabilities,
  ServerInfo,
  ServerUpdatePayload,
  ServerUpdatePhase,
  ServerUpdateStatus,
} from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

/** Which hop is down when the app cannot reach its data.
 * `network`: this machine has no usable network at all - DNS is dead, or
 * there is no route to the host. Nothing on the server side is implicated.
 * `gateway`: the HTTP origin itself is gone - the `aether gui` process that
 * serves the page died - so nothing answers at all.
 * `server`: the gateway answers but its SSH backend cannot reach
 * aether-server (it reports 503 "server unreachable: ..."). */
export type UnreachableKind = 'network' | 'gateway' | 'server'

/** The order the phases of one update run in. A terminal phase - failed
 * or cancelled - is not in it: those always win. */
const phaseOrder: Record<ServerUpdatePhase, number> = {
  scheduled: 1,
  applying: 2,
  restarting: 3,
  failed: 0,
  cancelled: 0,
}

/**
 * Whether the server is replacing its own binaries right now, which is
 * what makes a dropped connection a possible re-exec. A scheduled update
 * is deliberately not one: it can sit idle for days, and re-hydrating on
 * every network blip until then would cost far more than it saves. The
 * banner re-reads the status on any reconnect anyway, so a schedule that
 * applied while the tab was away is still picked up.
 */
export function serverUpdateApplying(progress: ServerUpdatePayload | null): boolean {
  return progress?.phase === 'applying' || progress?.phase === 'restarting'
}

/**
 * Whether a frame would move one update backwards. The server.update RPC
 * result and the event it publishes race, and the events arrive once per
 * workspace, so the same phase lands several times: without this a late
 * "applying" would overwrite the "restarting" already shown.
 */
function stalePhase(
  current: ServerUpdatePayload | null,
  next: ServerUpdatePayload,
): boolean {
  if (!current) return false
  if (phaseOrder[next.phase] === 0 || phaseOrder[current.phase] === 0) return false
  if (current.version !== next.version) return false
  return phaseOrder[next.phase] < phaseOrder[current.phase]
}

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
  /** The last server.update_status answer; null until the update banner
   * host reads it, and on a server too old to serve the method. */
  serverUpdate: ServerUpdateStatus | null
  /**
   * Why the last status read failed, or null. It is what separates a
   * server that answered "I cannot update myself" from one the dashboard
   * never reached, which are two different things to tell an admin.
   */
  serverUpdateError: string | null
  /** How far the running self-update has got, from the server.update feed
   * and the RPC results. Session-scoped: a reload re-reads the status. */
  serverUpdateProgress: ServerUpdatePayload | null
  setInfo: (info: ServerInfo) => void
  setCapabilities: (capabilities: GatewayCapabilities | null) => void
  setConnection: (state: ConnectionState) => void
  noteSeq: (seq: number) => void
  /** Forgets the cursor after the server's event log restarted under us. */
  resetSeq: () => void
  setHydrated: (hydrated: boolean, error?: string | null) => void
  setStreamDead: () => void
  setUnreachable: (kind: UnreachableKind | null) => void
  setServerUpdate: (status: ServerUpdateStatus) => void
  /** Records a failed status read, keeping the last good answer. */
  setServerUpdateFailed: (detail: string) => void
  /** Applies one update phase, from an event or from an RPC result. */
  applyServerUpdate: (payload: ServerUpdatePayload) => void
  resetConnection: () => void
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
  serverUpdate: null,
  serverUpdateError: null,
  serverUpdateProgress: null,
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
  setServerUpdate: (serverUpdate) =>
    set((s) => ({
      serverUpdate,
      serverUpdateError: null,
      // The status is re-read on every reconnect, which is how an update
      // ends: a server already running the version the phases were about
      // has finished, so the progress is history and must stop saying a
      // restart is coming.
      serverUpdateProgress:
        s.serverUpdateProgress &&
        bareVersion(s.serverUpdateProgress.version ?? '') ===
          bareVersion(serverUpdate.server_version)
          ? null
          : s.serverUpdateProgress,
    })),
  setServerUpdateFailed: (serverUpdateError) => set({ serverUpdateError }),
  applyServerUpdate: (payload) =>
    set((s) =>
      stalePhase(s.serverUpdateProgress, payload)
        ? {}
        : { serverUpdateProgress: payload },
    ),
  resetConnection: () =>
    set({
      connection: 'connecting',
      lastSeq: 0,
      hydrated: false,
      hydrationError: null,
      streamDead: false,
      unreachable: null,
    }),
})
