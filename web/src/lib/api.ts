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
  DiskUsage,
  DaemonStatusResult,
  EnvHarnessesResult,
  EnvScanStatus,
  GatewayCapabilities,
  ImageScaffoldResult,
  LinkRepoResult,
  LinkStatus,
  Member,
  Overlap,
  PresenceEntry,
  ProfilePreview,
  ProfilePushResult,
  ProfileRecommendation,
  ProfileStatus,
  PullResult,
  PullSwitchResult,
  RepoPushResult,
  RepoSyncResult,
  FileDiff,
  FileRead,
  FilesTreeResult,
  Run,
  RunPatch,
  Schedule,
  ServerInfo,
  ServerUpdateResult,
  ServerUpdateStatus,
  ServerUpdateWhen,
  SyncSessionState,
  SyncStatusResult,
  TerminalStatusResult,
  Template,
  TemplateLaunch,
  TimelinePage,
  TimelineQuery,
  UpdateApplyResult,
  UpdateBuildStatus,
  UpdateStatus,
  Workspace,
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
async function local<T>(
  verb: string,
  params: unknown = {},
  signal?: AbortSignal,
): Promise<T> {
  const token = bearer()
  const res = await fetch(`/local/v1/${verb}`, {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      ...(token ? { authorization: `Bearer ${token}` } : {}),
    },
    body: JSON.stringify(params),
    // Aborting closes the connection, which cancels the request context
    // on the gateway. A verb that walks the user's whole profile root
    // needs that: leaving the step must stop the work, not just stop
    // waiting for it.
    signal,
  })
  if (!res.ok) throw new ApiError(res.status, `${verb}: ${await failure(res)}`)
  return (await res.json()) as T
}

export interface LocalForward {
  run_id: string
  port: number
  local_port: number
}

export interface LocalForwardStartResult extends LocalForward {
  state: 'active'
}

export interface LocalForwardStopResult {
  run_id: string
  port: number
  state: 'stopped'
}

export interface LocalForwardStatusResult {
  forwards: Array<LocalForward & { conns: number }>
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

/** A profile scan's session; closing cancels the gateway process. */
export interface EnvScanSession {
  close: () => void
}

/** The start frame of a profile scan: the harness that runs the agent, and
 * optionally the repository folder so the project can inform the call. */
export interface ProfileScanRequest {
  harness: string
  repo_path?: string
}

/** A profile scan's output and status stream, ending in a recommendation. */
export interface ProfileScanHandlers {
  onOutput: (line: string) => void
  onStatus: (status: EnvScanStatus) => void
  /** The agent's proposal: which harnesses to import, and why. Nothing is
   * imported until the user approves and profile.push runs. */
  onResult: (recommendation: ProfileRecommendation) => void
  onError: (detail: string, outputTail?: string) => void
}

type ProfileScanFrame =
  | { type: 'output'; line: string }
  | { type: 'status'; status: EnvScanStatus }
  | { type: 'result'; recommendation?: ProfileRecommendation }
  | { type: 'error'; detail: string; output_tail?: string }

/**
 * Asks the chosen harness which of this machine's agent configuration is
 * worth bringing to the server. The agent sees paths and category counts
 * only, never file contents.
 */
function openProfileScan(
  req: ProfileScanRequest,
  h: ProfileScanHandlers,
): EnvScanSession {
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
    queueMicrotask(() => {
      if (!disposed) h.onError('connection failed')
    })
  }

  if (socket) {
    const ws = socket
    ws.onopen = () => {
      const start: Record<string, unknown> = {
        harness: req.harness,
        mode: 'profile',
      }
      if (req.repo_path) start.repo_path = req.repo_path
      ws.send(JSON.stringify(start))
    }
    ws.onmessage = (msg) => {
      if (settled || typeof msg.data !== 'string') return
      let frame: ProfileScanFrame
      try {
        frame = JSON.parse(msg.data) as ProfileScanFrame
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
            frame.recommendation
              ? h.onResult(frame.recommendation)
              : h.onError('the scan returned no recommendation'),
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
  runDelete: (runID: string) => call<unknown>('run.delete', { run_id: runID }),
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
  /** Whether the server can replace its own binaries, and what update is
   * in flight. Any member may read it; only an admin may call server.update. */
  serverUpdateStatus: () => call<ServerUpdateStatus>('server.update_status'),
  /** Asks the server to update itself to the latest release now, at the
   * next idle moment, or to cancel the pending one. */
  serverUpdate: (when: ServerUpdateWhen) =>
    call<ServerUpdateResult>('server.update', { when }),
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
  // Without a range this is the cumulative diff against the fork point; with
  // one it is the diff between two trees the server recorded, whose `base` is
  // then the `from` tree rather than the fork point.
  runPatch: (runID: string, range?: { from: string; to: string }) =>
    get<RunPatch>(
      `/run/${encodeURIComponent(runID)}/patch` +
        (range ? `?${new URLSearchParams(range).toString()}` : ''),
    ),
  filesTree: (params: { workspace_id: string; run_id?: string; path: string }) =>
    call<FilesTreeResult>('files.tree', params),
  filesRead: (params: { workspace_id: string; run_id?: string; path: string }) =>
    call<FileRead>('files.read', params),
  filesDiff: (runID: string, path: string) =>
    call<FileDiff>('files.diff', { run_id: runID, path }),
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
  localPullSwitch: (runID: string) =>
    local<PullSwitchResult>('pull.switch', { run_id: runID }),
  /** Pushes the workspace's base branch to the `aether` remote, seeding a
   * fresh workspace without leaving the app. */
  localRepoPush: (workspaceID?: string) =>
    local<RepoPushResult>('repo.push', { workspace_id: workspaceID }),
  /** Fast-forwards the workspace base branch from this machine's origin. */
  localRepoSync: (workspaceID?: string) =>
    local<RepoSyncResult>('repo.sync', { workspace_id: workspaceID }),
  localSyncStart: (runID: string, force?: boolean) =>
    local<SyncSessionState>('sync.start', { run_id: runID, force }),
  localSyncStop: (runID: string) =>
    local<SyncSessionState>('sync.stop', { run_id: runID }),
  localForwardStart: (runID: string, port: number) =>
    local<LocalForwardStartResult>('forward.start', { run_id: runID, port }),
  localForwardStop: (runID: string, port: number) =>
    local<LocalForwardStopResult>('forward.stop', { run_id: runID, port }),
  localForwardStatus: () => local<LocalForwardStatusResult>('forward.status'),
  localSyncStatus: () => local<SyncStatusResult>('sync.status'),
  localDaemonInstall: (server: string, repo: string) =>
    local<DaemonInstallResult>('daemon.install', { server, repo }),
  localDaemonStatus: () => local<DaemonStatusResult>('daemon.status'),
  /** Whether the CLI on this machine, and the server it talks to, are
   * behind the newest release. */
  localUpdateCheck: () => local<UpdateStatus>('update.check'),
  /** Replaces the aether binary on this machine with the newest release. */
  localUpdateApply: () => local<UpdateApplyResult>('update.apply'),
  /** Progress of a desktop-app rebuild started by update.apply. */
  localUpdateStatus: () => local<UpdateBuildStatus>('update.status'),
  localImageScaffold: (repo: string, kind: 'dockerfile' | 'devcontainer') =>
    local<ImageScaffoldResult>('image.scaffold', { repo, kind }),
  /** Which setup-capable harnesses this machine has on PATH, plus the
   * linked repository folder when the gateway knows exactly one. */
  envHarnesses: () => local<EnvHarnessesResult>('env.harnesses'),
  /** What `aether profile push --agent <harness>` would carry from this
   * machine, uploading nothing. It walks the whole profile root, so it
   * takes a signal: aborting stops the walk on the gateway too. */
  localProfilePreview: (harness: string, signal?: AbortSignal) =>
    local<ProfilePreview>('profile.preview', { harness }, signal),
  /** Pushes this member's configuration for one harness. There is no
   * allow-secret parameter: a scanner finding refuses the push, and the
   * override lives on the CLI. */
  localProfilePush: (harness: string) =>
    local<ProfilePushResult>('profile.push', { harness }),
  terminalStatus: () => call<TerminalStatusResult>('terminal.status', {}),
  terminalStop: () => call<unknown>('terminal.stop', {}),
  terminalSocket: (tab: string) =>
    socketURL(`/ws/terminal?tab=${encodeURIComponent(tab)}`),
  openProfileScan,
  eventsSocket: () => socketURL('/ws/events'),
  attachSocket: (runID: string) =>
    socketURL(`/ws/attach/${encodeURIComponent(runID)}`),
  attachShellSocket: (runID: string, tab: string) =>
    socketURL(
      `/ws/attach/${encodeURIComponent(runID)}?shell=${encodeURIComponent(tab)}`,
    ),
}

export type Api = typeof api
