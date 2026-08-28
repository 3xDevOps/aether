// The one place the dashboard talks to the server. Every endpoint the SPA
// uses lives here, so a change to the gateway's route table is a one-file
// edit. The contract is docs/local-gateway.md: POST /api/v1/<rpc.method> with
// the method's params as the body, bearer token from `aether gui`.

import type {
  AgentDefinition,
  AgentInfo,
  Approval,
  BudgetReport,
  DaemonInstallResult,
  DaemonStatusResult,
  DiskUsage,
  EnvScanFrame,
  EnvScanRequest,
  EnvScanResult,
  EnvScanStatus,
  EnvStatusResult,
  EnvironmentSource,
  GatewayCapabilities,
  HarnessStatus,
  ImageScaffoldResult,
  LinkRepoResult,
  LinkStatus,
  ManifestItem,
  Member,
  Overlap,
  PresenceEntry,
  ProfileStatus,
  PullResult,
  Run,
  RunPatch,
  Schedule,
  ServerInfo,
  SyncSessionState,
  SyncStatusResult,
  Template,
  TemplateLaunch,
  TimelinePage,
  TimelineQuery,
  ToolSnapshot,
  Workspace,
  WorkspaceSelector,
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

// Every request carries a token, loopback included: `aether gui` mints one
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

/**
 * A client-machine verb: POST /local/v1/<verb>. Only the local gateway
 * serves these (useCapability's hasLocal says which); failures carry the
 * same JSON-RPC error envelope as the proxied API.
 */
async function local<T>(verb: string, params: unknown = {}): Promise<T> {
  const token = bearer()
  const res = await fetch(`/local/v1/${verb}`, {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      ...(token ? { authorization: `Bearer ${token}` } : {}),
    },
    body: JSON.stringify(params),
  })
  if (!res.ok) throw new ApiError(res.status, `${verb}: ${await failure(res)}`)
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

/** What each /ws/envscan frame lands on. Output and status stream while
 * the scan runs; exactly one of result or error ends it. */
export interface EnvScanHandlers {
  /** One raw line of agent output, in arrival order. */
  onOutput: (line: string) => void
  /** A coarse status change: detecting, running, validating, retrying. */
  onStatus: (status: EnvScanStatus) => void
  /** The scan produced a validated Dockerfile and manifest pair. */
  onResult: (result: EnvScanResult) => void
  /** The scan failed for good, with the last output lines for diagnosis.
   * An unexpected close lands here too. */
  onError: (detail: string, outputTail?: string) => void
}

export interface EnvScanSession {
  /** Cancels the scan and its process; no handler fires afterward. */
  close: () => void
}

/**
 * openEnvScan runs one environment inventory on this machine through the
 * local gateway's /ws/envscan socket: one JSON start frame out, then
 * output and status frames in until a terminal result or error frame.
 * Framing mirrors the shell client (routes/shell/client.ts): a session
 * settles exactly once, and a close the caller asked for is not an
 * outcome.
 */
function openEnvScan(req: EnvScanRequest, h: EnvScanHandlers): EnvScanSession {
  let socket: WebSocket | null = null
  let disposed = false
  let settled = false

  const settle = (deliver: () => void) => {
    if (disposed || settled) return
    settled = true
    deliver()
  }

  try {
    socket = new WebSocket(socketURL('/ws/envscan'))
  } catch {
    settled = true
    // After the caller has its session handle; a constructor failure is a
    // dead connection.
    queueMicrotask(() => {
      if (!disposed) h.onError('connection failed')
    })
  }

  if (socket) {
    const ws = socket
    ws.onopen = () => {
      // EnvScanRequest with omitempty mirrored: absent keys, not empties.
      const start: Record<string, unknown> = {
        harness: req.harness,
        mode: req.mode,
      }
      if (req.previous_dockerfile) start.previous_dockerfile = req.previous_dockerfile
      if (req.previous_manifest_json) {
        start.previous_manifest_json = req.previous_manifest_json
      }
      if (req.feedback) start.feedback = req.feedback
      ws.send(JSON.stringify(start))
    }
    ws.onmessage = (msg) => {
      if (settled || typeof msg.data !== 'string') return
      let frame: EnvScanFrame
      try {
        frame = JSON.parse(msg.data) as EnvScanFrame
      } catch {
        return
      }
      switch (frame.type) {
        case 'output':
          h.onOutput(frame.line)
          break
        case 'status':
          h.onStatus(frame.status)
          break
        case 'result':
          settle(() =>
            h.onResult({ dockerfile: frame.dockerfile, manifest: frame.manifest }),
          )
          break
        case 'error':
          settle(() => h.onError(frame.detail, frame.output_tail))
          break
      }
    }
    ws.onerror = () => ws.close()
    ws.onclose = (ev) => {
      socket = null
      settle(() => h.onError(ev.reason || `connection closed (${ev.code})`))
    }
  }

  return {
    // Detach the handlers first: a close we asked for is not an outcome,
    // and the gateway cancels the scan when the socket goes.
    close: () => {
      disposed = true
      const ws = socket
      socket = null
      if (!ws) return
      ws.onopen = ws.onmessage = ws.onerror = ws.onclose = null
      ws.close()
    },
  }
}

// Only what the SPA actually calls; the team-feature methods land with the
// tickets that use them.
export const api = {
  serverInfo: () => call<ServerInfo>('server.info'),
  memberList: () =>
    call<{ members: Member[] }>('member.list').then((r) => r.members),
  runList: (params: { workspace_id?: string; active_only?: boolean } = {}) =>
    call<{ runs: Run[] }>('run.list', params).then((r) => r.runs),
  runGet: (runID: string) =>
    call<{ run: Run }>('run.get', { run_id: runID }).then((r) => r.run),
  runLaunch: (params: {
    workspace_id: string
    task?: string
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
  approvalList: (workspaceID: string, all = false) =>
    call<{ approvals: Approval[] }>('approval.list', {
      workspace_id: workspaceID,
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
  presenceHeartbeat: (workspaceID: string) =>
    call<{ ttl_seconds: number }>('presence.heartbeat', {
      workspace_id: workspaceID,
    }).then((r) => r.ttl_seconds),
  budgetGet: (workspaceID: string) =>
    call<BudgetReport>('budget.get', { workspace_id: workspaceID }),
  templateList: (workspaceID: string) =>
    call<{ templates: Template[] }>('template.list', {
      workspace_id: workspaceID,
    }).then((r) => r.templates),
  templateLaunch: (workspaceID: string, name: string) =>
    call<TemplateLaunch>('template.launch', { workspace_id: workspaceID, name }),
  workspaceTimeline: (query: TimelineQuery) =>
    call<TimelinePage>('workspace.timeline', query),
  runOverlaps: () =>
    call<{ overlaps: Overlap[] }>('run.overlaps').then((r) => r.overlaps),
  // Admin and membership: invites, approvals, colors, workspaces.
  memberInvite: (ttlSeconds?: number) =>
    call<{ code: string; expires_at: string }>('member.invite', {
      ttl_seconds: ttlSeconds,
    }),
  memberApprove: (memberID: string) =>
    call<{ member: Member }>('member.approve', { member_id: memberID }).then(
      (r) => r.member,
    ),
  memberRemove: (memberID: string) =>
    call<unknown>('member.remove', { member_id: memberID }),
  /** Sets the caller's own attribution color. */
  memberColor: (color: string) =>
    call<{ member: Member }>('member.color', { color }).then((r) => r.member),
  /** Sets another member's role; admin only, and never the last admin. */
  memberRole: (memberID: string, role: Member['role']) =>
    call<{ member: Member }>('member.role', { member_id: memberID, role }).then(
      (r) => r.member,
    ),
  workspaceAdd: (params: {
    name: string
    base_branch?: string
    environment?: unknown
  }) =>
    call<{ workspace: Workspace }>('workspace.add', params).then(
      (r) => r.workspace,
    ),
  workspaceGet: (workspaceID: string) =>
    call<{ workspace: Workspace }>('workspace.get', {
      workspace_id: workspaceID,
    }).then((r) => r.workspace),
  workspaceListFull: () =>
    call<{ workspaces: Workspace[] }>('workspace.list').then(
      (r) => r.workspaces,
    ),
  workspaceSettings: (params: { workspace_id: string; steer_others?: string }) =>
    call<{ workspace: Workspace }>('workspace.settings', {
      workspace_id: params.workspace_id,
      steer_others: params.steer_others ?? '',
    }).then((r) => r.workspace),
  // Workspace tool snapshots. Every method addresses the workspace by
  // exactly one of id or name, like the protocol's WorkspaceSelector.
  toolsList: (ws: WorkspaceSelector) =>
    call<{ snapshots: ToolSnapshot[] }>('workspace.tools.list', {
      workspace: ws,
    }).then((r) => r.snapshots),
  toolsVerify: (ws: WorkspaceSelector) =>
    call<unknown>('workspace.tools.verify', { workspace: ws }),
  toolsRollback: (ws: WorkspaceSelector, snapshotID: string) =>
    call<unknown>('workspace.tools.rollback', {
      workspace: ws,
      snapshot_id: snapshotID,
    }),
  toolsReset: (ws: WorkspaceSelector) =>
    call<unknown>('workspace.tools.reset', { workspace: ws, confirm: true }),
  // Budgets: the server clears a budget on a limit of zero or less, so
  // `clear` is spelled here rather than by every caller.
  budgetSet: (params: {
    workspace_id: string
    limit_usd?: number
    warn_usd?: number
    clear?: boolean
  }) =>
    call<BudgetReport>('budget.set', {
      workspace_id: params.workspace_id,
      limit_usd: params.clear ? 0 : (params.limit_usd ?? 0),
      warn_usd: params.warn_usd,
    }),
  // Templates and their cron schedules.
  templateSave: (params: {
    workspace_id: string
    name: string
    task: string
    harness: string
    mode?: string
  }) =>
    call<{ template: Template }>('template.save', params).then(
      (r) => r.template,
    ),
  templateDelete: (workspaceID: string, name: string) =>
    call<unknown>('template.delete', { workspace_id: workspaceID, name }),
  scheduleList: (workspaceID: string) =>
    call<{ schedules: Schedule[] }>('schedule.list', {
      workspace_id: workspaceID,
    }).then((r) => r.schedules),
  scheduleSave: (params: {
    workspace_id: string
    template: string
    cron: string
  }) =>
    call<{ schedule: Schedule }>('schedule.save', params).then(
      (r) => r.schedule,
    ),
  scheduleDelete: (workspaceID: string, template: string) =>
    call<unknown>('schedule.delete', { workspace_id: workspaceID, template }),
  // Harness profiles and custom agents.
  profileStatus: (harness: string) =>
    call<ProfileStatus>('profile.status', { harness }),
  profileRollback: (harness: string, snapshotID: string) =>
    call<unknown>('profile.rollback', { harness, snapshot_id: snapshotID }),
  agentList: () =>
    call<{ agents: AgentInfo[] }>('agent.list').then((r) => r.agents),
  agentRegister: (definition: AgentDefinition) =>
    call<unknown>('agent.register', { definition }),
  runProtect: (runID: string, protect: boolean) =>
    call<unknown>('run.protect', { run_id: runID, protected: protect }),
  runRelaunch: (runID: string) =>
    call<{ run: Run }>('run.relaunch', { run_id: runID }).then((r) => r.run),
  // The two endpoints that are not RPC methods: patch text is a read of a
  // working tree, and disk usage has no place on the frozen server.info
  // result. See docs/local-gateway.md.
  runPatch: (runID: string) =>
    get<RunPatch>(`/run/${encodeURIComponent(runID)}/patch`),
  disk: () => get<DiskUsage>('/disk'),
  capabilities: () => get<GatewayCapabilities>('/capabilities'),
  // The local gateway's client-machine verbs; see the `local` helper.
  localLinkStatus: () => local<LinkStatus>('link.status'),
  localLinkRepo: (repo: string, workspaceID?: string) =>
    local<LinkRepoResult>('link.repo', { repo, workspace_id: workspaceID }),
  // link.switch never succeeds: the gateway's SSH identity is fixed at
  // process start, so it answers the restart instruction as an error.
  localLinkSwitch: (name: string) => local<never>('link.switch', { name }),
  localPull: (runID: string) => local<PullResult>('pull', { run_id: runID }),
  localSyncStart: (runID: string, force?: boolean) =>
    local<SyncSessionState>('sync.start', { run_id: runID, force }),
  localSyncStop: (runID: string) =>
    local<SyncSessionState>('sync.stop', { run_id: runID }),
  localSyncStatus: () => local<SyncStatusResult>('sync.status'),
  localDaemonInstall: (server: string, repo: string) =>
    local<DaemonInstallResult>('daemon.install', { server, repo }),
  localDaemonStatus: () => local<DaemonStatusResult>('daemon.status'),
  localImageScaffold: (repo: string, kind: 'dockerfile' | 'devcontainer') =>
    local<ImageScaffoldResult>('image.scaffold', { repo, kind }),
  // Workspace environments: definitions live on the server (admin-guarded
  // wire methods), the inventory scan runs on this machine through the
  // local gateway.
  envStatus: (ws: WorkspaceSelector) =>
    call<EnvStatusResult>('env.status', { workspace: ws }),
  envSave: (params: {
    workspace: WorkspaceSelector
    dockerfile: string
    manifest: ManifestItem[]
    source: EnvironmentSource
    harness?: string
  }) =>
    call<{ version: number }>('env.save', {
      workspace: params.workspace,
      dockerfile: params.dockerfile,
      manifest: params.manifest,
      source: params.source,
      ...(params.harness ? { harness: params.harness } : {}),
    }).then((r) => r.version),
  /** Builds the given version, or the active/newest one when omitted;
   * progress rides the environment.build event stream. */
  envBuild: (ws: WorkspaceSelector, version?: number) =>
    call<{ version: number }>('env.build', {
      workspace: ws,
      ...(version ? { version } : {}),
    }).then((r) => r.version),
  /** Which setup-capable harnesses this machine has on PATH. */
  envHarnesses: () =>
    local<{ harnesses: HarnessStatus[] }>('env.harnesses').then(
      (r) => r.harnesses,
    ),
  openEnvScan,
  eventsSocket: () => socketURL('/ws/events'),
  attachSocket: (runID: string) =>
    socketURL(`/ws/attach/${encodeURIComponent(runID)}`),
  /** The unified workspace-shell socket; the header frame picks the mode. */
  shellSocket: () => socketURL('/ws/shell'),
}

export type Api = typeof api
