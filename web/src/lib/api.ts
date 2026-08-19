// The one place the dashboard talks to the server. Every endpoint the SPA
// uses lives here, so a change to the gateway's route table is a one-file
// edit. The contract is docs/dashboard-api.md: POST /api/v1/<rpc.method> with
// the method's params as the body, bearer token from `aether dash`.

import type {
  Approval,
  BudgetReport,
  DiskUsage,
  Member,
  Overlap,
  PresenceEntry,
  Run,
  RunPatch,
  ServerInfo,
  Session,
  Template,
  TemplateLaunch,
  TimelinePage,
  TimelineQuery,
} from '@/lib/types'

export const API_BASE = '/api/v1'

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

// Every request carries a token, loopback included: `aether dash` mints one
// and opens a tokened URL. Keep the token out of the address bar once we have
// it.
const tokenKey = 'aether.token'

function bearer(): string | null {
  if (typeof window === 'undefined') return null
  const fromURL = new URLSearchParams(window.location.search).get('token')
  if (fromURL) {
    window.sessionStorage.setItem(tokenKey, fromURL)
    const clean = new URL(window.location.href)
    clean.searchParams.delete('token')
    window.history.replaceState({}, '', clean.toString())
    return fromURL
  }
  return window.sessionStorage.getItem(tokenKey)
}

async function call<T>(method: string, params: unknown = {}): Promise<T> {
  const token = bearer()
  const res = await fetch(`${API_BASE}/${method}`, {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      ...(token ? { authorization: `Bearer ${token}` } : {}),
    },
    body: JSON.stringify(params),
  })
  if (!res.ok) throw new ApiError(res.status, `${method}: ${await failure(res)}`)
  return (await res.json()) as T
}

async function get<T>(path: string): Promise<T> {
  const token = bearer()
  const res = await fetch(`${API_BASE}${path}`, {
    headers: token ? { authorization: `Bearer ${token}` } : {},
  })
  if (!res.ok) throw new ApiError(res.status, `${path}: ${await failure(res)}`)
  return (await res.json()) as T
}

// Failures carry the JSON-RPC error object: {"error":{"code":-32001,...}}.
async function failure(res: Response): Promise<string> {
  try {
    const body = (await res.json()) as { error?: { message?: string } }
    if (body.error?.message) return body.error.message
  } catch {
    // Not every failure has a JSON body (a proxy 502, for instance).
  }
  return `${res.status} ${res.statusText}`
}

/** WebSocket URL for a gateway path, carrying the bearer token when set. */
export function socketURL(path: string): string {
  const url = new URL(path, window.location.href)
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
  const token = bearer()
  if (token) url.searchParams.set('token', token)
  return url.toString()
}

// Only what the SPA actually calls; the team-feature methods land with the
// tickets that use them.
export const api = {
  serverInfo: () => call<ServerInfo>('server.info'),
  sessionList: (workspaceID?: string) =>
    call<{ sessions: Session[] }>('session.list', {
      workspace_id: workspaceID,
    }).then((r) => r.sessions),
  memberList: () =>
    call<{ members: Member[] }>('member.list').then((r) => r.members),
  runList: (params: { session_id?: string; active_only?: boolean } = {}) =>
    call<{ runs: Run[] }>('run.list', params).then((r) => r.runs),
  runGet: (runID: string) =>
    call<{ run: Run }>('run.get', { run_id: runID }).then((r) => r.run),
  runLaunch: (params: {
    session_id: string
    task: string
    harness: string
    mode?: string
  }) => call<{ run: Run }>('run.launch', params).then((r) => r.run),
  runKill: (runID: string) => call<unknown>('run.kill', { run_id: runID }),
  runPause: (runID: string) => call<unknown>('run.pause', { run_id: runID }),
  runResume: (runID: string) => call<unknown>('run.resume', { run_id: runID }),
  runInject: (runID: string, message: string) =>
    call<unknown>('run.inject', { run_id: runID, message }),
  runClose: (runID: string, outcome: 'merged' | 'abandoned') =>
    call<{ run: Run }>('run.close', { run_id: runID, outcome }).then((r) => r.run),
  runHandoff: (runID: string, toMemberID: string) =>
    call<unknown>('run.handoff', { run_id: runID, to_member_id: toMemberID }),
  approvalList: (sessionID: string, all = false) =>
    call<{ approvals: Approval[] }>('approval.list', {
      session_id: sessionID,
      all,
    }).then((r) => r.approvals),
  approvalDecide: (runID: string, requestID: string, approve: boolean) =>
    call<{ approval: Approval }>('approval.decide', {
      run_id: runID,
      request_id: requestID,
      approve,
    }).then((r) => r.approval),
  presenceRoster: () =>
    call<{ members: PresenceEntry[] }>('presence.roster').then((r) => r.members),
  presenceHeartbeat: (sessionID: string) =>
    call<{ ttl_seconds: number }>('presence.heartbeat', {
      session_id: sessionID,
    }).then((r) => r.ttl_seconds),
  budgetGet: (sessionID: string) =>
    call<BudgetReport>('budget.get', { session_id: sessionID }),
  templateList: (sessionID: string) =>
    call<{ templates: Template[] }>('template.list', {
      session_id: sessionID,
    }).then((r) => r.templates),
  templateLaunch: (sessionID: string, name: string) =>
    call<TemplateLaunch>('template.launch', { session_id: sessionID, name }),
  sessionTimeline: (query: TimelineQuery) =>
    call<TimelinePage>('session.timeline', query),
  runOverlaps: () =>
    call<{ overlaps: Overlap[] }>('run.overlaps').then((r) => r.overlaps),
  // The two endpoints that are not RPC methods: patch text is a read of a
  // working tree, and disk usage has no place on the frozen server.info
  // result. See docs/dashboard-api.md.
  runPatch: (runID: string) =>
    get<RunPatch>(`/run/${encodeURIComponent(runID)}/patch`),
  disk: () => get<DiskUsage>('/disk'),
  eventsSocket: () => socketURL('/ws/events'),
  attachSocket: (runID: string) =>
    socketURL(`/ws/attach/${encodeURIComponent(runID)}`),
}

export type Api = typeof api
