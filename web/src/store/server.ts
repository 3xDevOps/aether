import { bareVersion } from '@/lib/format'
import { compareVersions } from '@/lib/version'
import type { ConnectionState } from '@/lib/stream'
import type {
  AgentInfo,
  EdgePath,
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
 * serves the page died - so nothing answers at all. Desktop only: it is the
 * only gateway the user can restart.
 * `tailnet`: the same silence on a phone, where the server itself serves the
 * page over the tailnet, so the hop that died is the tailnet or the host.
 * `server`: the gateway answers but its SSH backend cannot reach
 * aether-server (it reports 503 "server unreachable: ..."). The next nine
 * are that failure on the edge path, told apart by the refusal's kind:
 * `edge` could not be reached, `edge-server` holds no live registration
 * for the server, `edge-not-claimed` holds one for a server nobody has
 * claimed, `edge-refused` refused it for another reason,
 * `signed-out` has no valid sign-in for this machine, `not-member` means
 * the edge or the server no longer counts the account as a member, the
 * server refused this device as `device-revoked` or `device-pending`, or
 * for another reason as `server-refused`. */
export type UnreachableKind =
  | 'network'
  | 'gateway'
  | 'tailnet'
  | 'server'
  | 'edge'
  | 'edge-server'
  | 'edge-not-claimed'
  | 'edge-refused'
  | 'signed-out'
  | 'not-member'
  | 'device-revoked'
  | 'device-pending'
  | 'server-refused'
  | 'refused'
  | 'identity'

/** The order the phases of one update run in. A terminal phase - failed
 * or cancelled - is not in it: those always win. */
const phaseOrder: Record<ServerUpdatePhase, number> = {
  scheduled: 1,
  preflight: 2,
  applying: 3,
  restarting: 4,
  verifying: 5,
  applied: 0,
  failed: 0,
  rolled_back: 0,
  cancelled: 0,
}

/** The phases between the decision to update and the new process committing. */
export function serverInFlight(
  phase: string | undefined,
): phase is 'preflight' | 'applying' | 'restarting' | 'verifying' {
  return phase === 'preflight' || phase === 'applying' || phase === 'restarting' || phase === 'verifying'
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
  return serverInFlight(progress?.phase)
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

/**
 * Whether a frame arrives after its own update settled. The server.update
 * result can land after the failed or rolled_back that attempt already
 * published; a new attempt starts at scheduled or preflight, and a click
 * clears the settled progress before it asks.
 */
function settledAlready(current: ServerUpdatePayload | null, next: ServerUpdatePayload): boolean {
  if (!current || phaseOrder[current.phase] !== 0 || current.version !== next.version) return false
  return next.phase === 'applying' || next.phase === 'restarting' || next.phase === 'verifying'
}

/**
 * The status is re-read on every reconnect, which is how an update ends. A
 * server that records phases is the authority on its own update; an older
 * one has finished once it runs the version the phases were about.
 */
function progressAfterStatus(
  progress: ServerUpdatePayload | null,
  at: number,
  status: ServerUpdateStatus,
): Pick<ServerSlice, 'serverUpdateProgress' | 'serverUpdateProgressAt'> {
  if (status.phase && serverInFlight(status.phase)) {
    if (progress?.phase === status.phase) return { serverUpdateProgress: progress, serverUpdateProgressAt: at }
    const next: ServerUpdatePayload = { phase: status.phase, version: status.to_version || progress?.version }
    if (stalePhase(progress, next)) return { serverUpdateProgress: progress, serverUpdateProgressAt: at }
    return { serverUpdateProgress: next, serverUpdateProgressAt: Date.now() }
  }
  if (!progress) return { serverUpdateProgress: null, serverUpdateProgressAt: at }
  const scheduledStill = progress.phase === 'scheduled' && status.pending !== undefined
  if (status.phase) {
    return scheduledStill
      ? { serverUpdateProgress: progress, serverUpdateProgressAt: at }
      : { serverUpdateProgress: null, serverUpdateProgressAt: 0 }
  }
  const version = progress.version ?? ''
  const order = compareVersions(status.server_version, version)
  const arrived = order === null ? bareVersion(version) === bareVersion(status.server_version) : order >= 0
  return arrived ? { serverUpdateProgress: null, serverUpdateProgressAt: 0 } : { serverUpdateProgress: progress, serverUpdateProgressAt: at }
}

export interface ServerSlice {
  info: ServerInfo | null
  /**
   * Identity of the authenticated server/member that populated the caches.
   * It survives ordinary reconnects so transient outages do not discard
   * drafts, but changes trigger a file-cache reset during hydration.
   */
  identityKey: string | null
  /** What the gateway can do; null until fetched, and stays null on a
   * legacy server that does not serve the endpoint. */
  capabilities: GatewayCapabilities | null
  connection: ConnectionState
  /** Changes whenever a caller needs to restart the connection lifecycle. */
  connectionEpoch: number
  /** Event-log generation used to scope caches whose keys include run IDs. */
  terminalCacheEpoch: number
  /** Highest event sequence applied; the cursor a reconnect replays from. */
  lastSeq: number
  hydrated: boolean
  hydrationError: string | null
  /** The stream stopped for good - a dead token - and nothing retries. */
  streamDead: boolean
  /** Which hop failed on the latest request to the server, or null when
   * the latest one reached it. Reset with the connection, so a server
   * switched to never inherits another server's failure. */
  unreachable: UnreachableKind | null
  /** Where `unreachable` was measured: the gateway's edge path, or only
   * the server id of the link. A failure measured on another server than
   * the one now linked is not this server's. */
  unreachableAt: Partial<EdgePath> | null
  /** The approval code a device-pending refusal carried, which the server
   * sends only when nobody can approve the device in the app. */
  approvalCode: string | null
  /** The last server.update_status answer; null until the update banner
   * host reads it, and on a server too old to serve the method. */
  serverUpdate: ServerUpdateStatus | null
  /**
   * Why the last status read failed, or null. It is what separates a
   * server that answered "I cannot update yourself" from one the dashboard
   * never reached, which are two different things to tell an admin.
   */
  serverUpdateError: string | null
  /** How far the running self-update has got, from the server.update feed
   * and the RPC results. Session-scoped: a reload re-reads the status. */
  serverUpdateProgress: ServerUpdatePayload | null
  /** When the progress last changed phase, in ms; what times out a server
   * that never comes back. */
  serverUpdateProgressAt: number
  agentList: { identity: string | null; agents: AgentInfo[] } | null
  setAgentList: (identity: string | null, agents: AgentInfo[]) => void
  setInfo: (info: ServerInfo) => void
  setIdentityKey: (identityKey: string) => void
  setCapabilities: (capabilities: GatewayCapabilities | null) => void
  setConnection: (state: ConnectionState) => void
  noteSeq: (seq: number) => void
  /** Forgets the cursor after the server's event log restarted under us. */
  resetSeq: () => void
  setHydrated: (hydrated: boolean, error?: string | null) => void
  setStreamDead: () => void
  setUnreachable: (kind: UnreachableKind | null, approvalCode?: string, at?: Partial<EdgePath>) => void
  setServerUpdate: (status: ServerUpdateStatus) => void
  /** Records a failed status read, keeping the last good answer. */
  setServerUpdateFailed: (detail: string) => void
  /** Applies one update phase, from an event or from an RPC result. */
  applyServerUpdate: (payload: ServerUpdatePayload) => void
  /** Forgets a settled update before a new attempt is asked for. */
  clearSettledServerUpdate: () => void
  resetConnection: () => void
  reconnect: () => void
}

export const createServerSlice: SliceCreator<ServerSlice> = (set) => {
  const resetConnection = () =>
    set({
      connection: 'connecting',
      lastSeq: 0,
      hydrated: false,
      hydrationError: null,
      streamDead: false,
      unreachable: null,
      unreachableAt: null,
      approvalCode: null,
    })
  return {
    info: null,
    identityKey: null,
    capabilities: null,
    connection: 'connecting',
    connectionEpoch: 0,
    terminalCacheEpoch: 0,
    lastSeq: 0,
    hydrated: false,
    hydrationError: null,
    streamDead: false,
    unreachable: null,
    unreachableAt: null,
    approvalCode: null,
    serverUpdate: null,
    serverUpdateError: null,
    serverUpdateProgress: null,
    serverUpdateProgressAt: 0,
    agentList: null,
    setAgentList: (identity, agents) => set({ agentList: { identity, agents } }),
    setInfo: (info) => set({ info }),
    setIdentityKey: (identityKey) => set({ identityKey }),
    setCapabilities: (capabilities) => set({ capabilities }),
    setConnection: (connection) => set({ connection }),
    noteSeq: (seq) =>
      set((s) => (seq > s.lastSeq ? { lastSeq: seq } : {})),
    resetSeq: () =>
      set((s) => ({
        lastSeq: 0,
        terminalCacheEpoch: s.terminalCacheEpoch + 1,
        sessionLogs: {},
      })),
    setHydrated: (hydrated, error = null) =>
      set({ hydrated, hydrationError: error }),
    setStreamDead: () => set({ streamDead: true }),
    setUnreachable: (unreachable, approvalCode, at) =>
      set({ unreachable, unreachableAt: unreachable ? at ?? null : null, approvalCode: approvalCode ?? null }),
    setServerUpdate: (serverUpdate) =>
      set((s) => ({
        serverUpdate,
        serverUpdateError: null,
        ...progressAfterStatus(s.serverUpdateProgress, s.serverUpdateProgressAt, serverUpdate),
      })),
    setServerUpdateFailed: (serverUpdateError) => set({ serverUpdateError }),
    applyServerUpdate: (payload) =>
      set((s) =>
        stalePhase(s.serverUpdateProgress, payload) || settledAlready(s.serverUpdateProgress, payload)
          ? {}
          : {
              serverUpdateProgress: payload,
              serverUpdateProgressAt:
                s.serverUpdateProgress?.phase === payload.phase ? s.serverUpdateProgressAt : Date.now(),
            },
      ),
    clearSettledServerUpdate: () =>
      set((s) =>
        s.serverUpdateProgress && phaseOrder[s.serverUpdateProgress.phase] === 0
          ? { serverUpdateProgress: null, serverUpdateProgressAt: 0 }
          : {},
      ),
    resetConnection,
    reconnect: () => {
      resetConnection()
      set((s) => ({ connectionEpoch: s.connectionEpoch + 1 }))
    },
  }
}
