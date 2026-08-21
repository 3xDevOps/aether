// Wire types. These mirror internal/protocol/wire.go and internal/events;
// field names are the JSON names the server sends.

export type RunStatus =
  | 'queued'
  | 'provisioning'
  | 'running'
  | 'needs-attention'
  | 'merged'
  | 'abandoned'
  | 'failed'
  | 'interrupted'

export interface Run {
  id: string
  session_id: string
  member_id: string
  task: string
  harness: string
  mode: string
  status: RunStatus
  branch: string
  protected?: boolean
  created_at: string
  started_at: string | null
  finished_at: string | null
  profile_snapshot_id?: string
  /** Last run.status reason, sanitized like the event payload. */
  reason?: string
  /** Decorated by the gateway from the scheduler; absent on legacy servers. */
  paused?: boolean
}

export interface Session {
  id: string
  workspace_id: string
  name: string
  base_branch: string
  steer_others?: string
  created_at: string
}

export interface Workspace {
  id: string
  name: string
  created_at: string
}

export interface Member {
  id: string
  display_name: string
  color: string
  role: 'viewer' | 'collaborator' | 'admin'
  pending?: boolean
}

export interface ServerInfo {
  server_version: string
  protocol_version: string
  time: string
  dashboard_port: number
  member: Member
  tailnet_hostname?: string
  tailnet_identity_auth?: boolean
  /** Data-directory usage, when the gateway reports it. */
  disk?: DiskUsage
}

export interface DiskUsage {
  used_bytes: number
  total_bytes: number
  /** What an unprivileged writer can still claim; the scheduler's floor. */
  free_bytes: number
  /** The three directories that grow without bound. */
  worktree_bytes: number
  transcript_bytes: number
  database_bytes: number
}

/**
 * GET /api/v1/capabilities - what this gateway can do. Legacy remote
 * monitors do not serve it; a null result means "assume the remote
 * allowlist" on the client.
 */
export interface GatewayCapabilities {
  gateway: string
  methods: string[]
  ws: string[]
  local?: string[]
}

export interface Event {
  id: string
  seq: number
  time: string
  session_id: string
  run_id: string
  actor_id: string
  type: string
  payload: unknown
}

export interface RunStatusPayload {
  from?: RunStatus
  to: RunStatus
  reason?: string
}

// Team surfaces: the approval inbox, the presence roster, cost and budgets,
// and the session timeline (internal/protocol approval.go, cost.go,
// timeline.go).

export type ApprovalDecision = 'requested' | 'approved' | 'denied'

export interface Approval {
  id: string
  session_id: string
  run_id: string
  action: string
  detail?: string
  decision: ApprovalDecision
  decided_by?: string
  created_at: string
  decided_at?: string
}

/** One present member. `watching` holds the runs they have attached to. */
export interface PresenceEntry {
  member_id: string
  state: 'online' | 'watching' | 'offline'
  watching?: string[]
  last_seen: string
}

/**
 * Aggregated usage. `unmetered_runs` counts runs whose usage was never
 * measured, so while it is non-zero every total here is a floor.
 */
export interface CostRollup {
  runs: number
  metered_runs: number
  unmetered_runs: number
  input_tokens: number
  output_tokens: number
  cost_usd: number
}

export interface Budget {
  session_id: string
  limit_usd: number
  warn_usd?: number
  override?: boolean
  updated_by?: string
  updated_at?: string
}

export type BudgetState = 'ok' | 'warn' | 'exceeded'

/** A session's budget with its state and the spend behind it. */
export interface BudgetReport {
  session_id: string
  budget?: Budget
  state: BudgetState
  spend: CostRollup
  advisory?: boolean
}

/** One page of session history, oldest first. */
export interface TimelinePage {
  events: Event[]
  next_seq: number
  more: boolean
}

export interface TimelineQuery {
  session_id: string
  run_id?: string
  member_id?: string
  types?: string[]
  after_seq?: number
  limit?: number
}

/** One file in a `run.diff` snapshot. */
export interface FileDiffStat {
  path: string
  additions: number
  deletions: number
}

export interface RunDiffPayload {
  files: FileDiffStat[]
}

/** One other active run touching files a run also touches. */
export interface OverlapPeer {
  run_id: string
  member_id: string
  files: string[]
}

/** One run's whole overlap set, as `run.overlaps` reports it. */
export interface Overlap {
  run_id: string
  with: OverlapPeer[]
}

/** The `run.overlap` event: the envelope run's overlaps at the moment they
 * changed, without the member ids the RPC result carries. */
export interface OverlapPayload {
  with?: { run_id: string; files: string[] }[]
}

/** GET /api/v1/run/<id>/patch - the run's diff against its fork point. */
export interface RunPatch {
  run_id: string
  base: string
  patch: string
  truncated: boolean
}

/** Wire form of a task template (internal/protocol/template.go). */
export interface Template {
  id: string
  session_id: string
  name: string
  task: string
  harness: string
  mode: string
  params?: Record<string, string>
  budget_usd?: number
  created_at: string
}

export interface TemplateLaunch {
  run: Run
  base_branch: string
  base_age?: string
}

/** The first line a client sends on /ws/events. */
export interface SubscribeRequest {
  session_id?: string
  run_id?: string
  types?: string[]
  replay?: boolean
  after_seq?: number
}
