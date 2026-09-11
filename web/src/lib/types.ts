// Wire types. These mirror internal/protocol/wire.go and internal/events;
// field names are the JSON names the server sends.

export type RunStatus =
  | 'queued'
  | 'provisioning'
  | 'running'
  | 'needs-attention'
  | 'completed'
  | 'merged'
  | 'abandoned'
  | 'failed'
  | 'interrupted'

export interface Run {
  id: string
  workspace_id: string
  member_id: string
  /** Account backing the run; absent on servers predating account sharing. */
  account_member_id?: string
  task: string
  /** Latest terminal title, omitted by older servers and for empty titles. */
  title?: string
  harness: string
  mode: string
  status: RunStatus
  branch: string
  last_commit?: string
  last_commit_at?: string | null
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
export interface Workspace {
  id: string
  name: string
  base_branch: string
  steer_others?: string
  created_at: string
  /** The upstream git URL a run checkout's `origin` remote points at;
   * absent until a clone with an origin is linked. */
  origin?: string
}

export interface Member {
  id: string
  display_name: string
  color: string
  role: 'viewer' | 'collaborator' | 'admin'
  pending?: boolean
  /** The name commits made in this member's runs are authored as; absent
   * until the member sets one, when the server falls back. */
  git_name?: string
  /** The email address those commits carry; absent until set. */
  git_email?: string
}

export interface AccountAccess {
  /** Accounts the caller may select at launch, including their own. */
  accounts: Member[]
  /** Members the caller has allowed to use their account. */
  shared_with: Member[]
}

export interface ServerInfo {
  server_version: string
  protocol_version: string
  time: string
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
  /** The four directories that grow without bound. */
  worktree_bytes: number
  transcript_bytes: number
  database_bytes: number
  /** The bare workspace repos; absent on servers predating the component. */
  repo_bytes?: number
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
  /** The CLI build serving this gateway; absent before the field existed. */
  version?: string
  commit?: string
}

/** The member's persistent environment terminal status. */
export interface TerminalStatusResult {
  running: boolean
  image?: string
  saved_image?: string
  started_at?: string
  tabs?: string[]
}

export interface EnvSaveResult {
  image: string
}

export interface Event {
  id: string
  seq: number
  time: string
  workspace_id: string
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

export interface GitBranchPayload {
  workspace_id: string
  branch: string
  commit: string
}

export interface RunTitlePayload {
  title: string
}
export interface RunProtectedPayload {
  protected: boolean
}

// Team surfaces: the approval inbox, the presence roster, cost and budgets,
// and the workspace timeline (internal/protocol approval.go, cost.go,
// timeline.go).

export type ApprovalDecision = 'requested' | 'approved' | 'denied'

export interface Approval {
  id: string
  workspace_id: string
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
  workspace_id: string
  limit_usd: number
  warn_usd?: number
  override?: boolean
  updated_by?: string
  updated_at?: string
}

export type BudgetState = 'ok' | 'warn' | 'exceeded'

/** A workspace's budget with its state and the spend behind it. */
export interface BudgetReport {
  workspace_id: string
  budget?: Budget
  state: BudgetState
  spend: CostRollup
  advisory?: boolean
}

/** One page of workspace history, oldest first. */
export interface TimelinePage {
  events: Event[]
  next_seq: number
  more: boolean
}

export interface TimelineQuery {
  workspace_id: string
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
  /** The git tree of the whole worktree at this snapshot. */
  tree?: string
  /**
   * The previous snapshot's tree, or the run's fork-point tree for the first
   * snapshot. Diffing `parent_tree` to `tree` is what this interval changed.
   * Both are absent on events from a server that predates per-snapshot trees.
   */
  parent_tree?: string
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

/** One immediate child returned by files.tree. */
export interface FileTreeEntry {
  name: string
  kind: 'file' | 'dir'
  size: number
}

export interface FilesTreeResult {
  entries: FileTreeEntry[]
}

/** One files.read response. */
export interface FileRead {
  content: string
  truncated: boolean
  binary: boolean
  size: number
  revision: string
  writable: boolean
}

/** One files.diff response. */
export interface FileDiff {
  patch: string
  truncated: boolean
}

/** Wire form of a task template (internal/protocol/template.go). */
export interface Template {
  id: string
  workspace_id: string
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
  workspace_id?: string
  run_id?: string
  types?: string[]
  replay?: boolean
  after_seq?: number
}

/** Wire form of a cron schedule (internal/protocol/template.go). */
export interface Schedule {
  id: string
  workspace_id: string
  template: string
  cron: string
  member_id: string
  created_at: string
  last_fire_at?: string | null
  next_fire_at?: string | null
}

/** Addresses a workspace by exactly one of id or name. */
export interface WorkspaceSelector {
  id?: string
  name?: string
}


/** One entry of agent.list; source is who supplied the harness. */
export interface AgentInfo {
  name: string
  source: 'shipped' | 'member'
  /** Whether the account's persistent environment contains the executable. */
  installed?: boolean
  /** Vendor installer command for shipped harnesses, when available. */
  install_script?: string
}

/** A member-supplied custom harness launch definition (agent.register). */
export interface AgentDefinition {
  name: string
  executable?: string
  tui_args?: string[]
  headless_args?: string[]
  profile_root?: string
  credential_paths?: string[]
  deny_names?: string[]
}

export interface ConfigRoot {
  harness: string
  path: string
}

export interface ConfigFile {
  path: string
  content_base64: string
  mode: number
}

export interface ConfigImportParams {
  harness: string
  files: ConfigFile[]
}

export interface ConfigExclusion {
  path: string
  reason: string
  detail?: string
}

export interface ConfigImportResult {
  harness: string
  files: number
  bytes: number
  excluded: ConfigExclusion[]
}

// The local gateway's client-machine verbs, POST /local/v1/<verb>
// (internal/localgw/local.go). Only a gateway with the user's repository
// and SSH key serves these; useCapability's hasLocal gates every caller.

/** link.status: whether this gateway has a linked server and repository. */
export interface LinkStatus {
  /** A server address is configured, even when no repository is linked. */
  server_configured: boolean
  linked: boolean
  addr: string
  user: string
  repo: string
  /**
   * Named server profiles from `aether link --name`; absent when none.
   * `repo` is the profile's own clone, absent when it has none.
   */
  links?: { name: string; addr: string; repo?: string }[]
  /** The profile this gateway runs on; absent on the top-level link. */
  active?: string
}

/** link.apply: the server identity linked to this local gateway. */
export interface LinkApplyResult {
  addr: string
  user: string
  member: { id: string; display_name: string; role: string }
  key_generated?: string
}

/** link.repo: the repo just linked and the git remote written into it. */
export interface LinkRepoResult {
  repo: string
  remote: string
  url: string
  /** The workspace's upstream origin afterwards, whether it was already
   * recorded or taken from this clone; absent when neither has one. */
  origin?: string
}

/** github.connect: the account the environment terminal is logged in to,
 * and the signing key now registered on it. */
export interface GitHubConnectResult {
  login: string
  signing_key: string
  fingerprint: string
}

/** github.probe status: gh is usable, absent, present but unrunnable, or
 * older than the login check can read. */
export type GitHubCLIStatus = 'ok' | 'missing' | 'broken' | 'outdated'

/** github.probe: the gh in the member's environment terminal, before they
 * are told to log in with it. The two remedies are empty while gh is
 * usable; `admin_remedy` is set only when the server's own standard image
 * is the one without a usable gh. */
export interface GitHubProbeResult {
  status: GitHubCLIStatus
  version?: string
  minimum: string
  /** What gh, or the container that could not run it, printed. */
  detail?: string
  /** The image the terminal container runs, and the member's own saved
   * one when they have it. They differ while a container outlives the
   * image it should be on. */
  image: string
  saved_image?: string
  /** Where gh resolved, present only when that is a file inside the
   * member's own environment home and therefore outlives every image. */
  path?: string
  remedy?: string
  admin_remedy?: string
}

/** pull: the run branch fetched into the linked repository. */
export interface PullResult {
  branch: string
  ref: string
  output: string
  current: boolean
  dirty: boolean
}

/** git.identity: the `user.name` and `user.email` this machine's git
 * resolves. Either is empty when the key is unset. */
export interface GitIdentity {
  name: string
  email: string
}

/** pull.switch: the run branch now checked out locally. */
export interface PullSwitchResult {
  branch: string
}
/** How the clone's base branch compared with the workspace's copy. */
export type RepoPushState = 'pushed' | 'up-to-date' | 'behind' | 'diverged'

/** repo.push: the comparison the gateway made, and the push if one ran. */
export interface RepoPushResult {
  branch: string
  remote: string
  state: RepoPushState
  local_commit: string
  /** Empty when the workspace has no such branch yet. */
  workspace_commit: string
  ahead: number
  behind: number
  output: string
}

/** repo.fast-forward: the clone's base branch moved up to the workspace. */
export interface RepoFastForwardResult {
  branch: string
  commit: string
  current: boolean
  dirty: boolean
  output: string
}

/** repo.sync: the workspace base branch fast-forwarded from origin. */
export interface RepoSyncResult {
  branch: string
  output: string
}


/** sync.start / sync.stop: one run's overlay state after the verb. */
export interface SyncSessionState {
  run_id: string
  state: string
}

/** One background sync session as sync.status reports it. */
export interface SyncSessionStatus {
  run_id: string
  state: string
  /** Describes a paused-on-conflict session; null otherwise. */
  conflict: string | null
}

export interface SyncStatusResult {
  sessions: SyncSessionStatus[]
}

/** daemon.install: where the sync-daemon unit landed and how to enable it. */
export interface DaemonInstallResult {
  unit_path: string
  note: string
}

export interface DaemonStatusResult {
  installed: boolean
  unit_path: string
}


/**
 * update.check: one release-check answer for the CLI on this machine
 * (internal/selfupdate). `dev` and `disabled` both mean no release was
 * resolved - a local build, or AETHER_NO_UPDATE_CHECK set - and neither
 * ever reports an update.
 */
export interface UpdateCheck {
  /** The running version; "dev" for a local build. */
  version: string
  commit: string
  /** The newest release tag; empty when nothing was checked. */
  latest?: string
  update_available: boolean
  /** The release asset for this platform, aether-<goos>-<goarch>. */
  asset?: string
  release_url?: string
  dev: boolean
  disabled: boolean
  /** False on Windows, where the binary cannot replace itself. */
  can_self_update: boolean
  checked_at: string
}

/** update.check: the CLI answer plus how the server it talks to compares. */
export interface UpdateStatus {
  cli: UpdateCheck
  /** Empty when the server did not answer; server_error then says why. */
  server_version: string
  server_behind: boolean
  /**
   * Why the server half is unknown. The CLI half is about a binary on this
   * machine, so it is answered in full even when the SSH hop is down.
   */
  server_error?: string
  /** The desktop shell spawned this gateway, so it can restart it. */
  supervised: boolean
  /**
   * The error from the last desktop-app rebuild that failed, persisted by
   * the gateway to a file. Absent when the last rebuild succeeded or none
   * has run.
   */
  shell_build_error?: string
  /** The binary update.apply replaces, symlinks resolved. Absent when the
   * gateway could not probe it; install_method is absent with it. */
  cli_path?: string
  /**
   * How update.apply gets to write cli_path. `direct`: its directory is
   * writable and the update just happens. `admin-prompt`: macOS shows its
   * administrator password dialog first. `manual`: the gateway cannot
   * replace it (a root-owned directory on Linux, or Windows), so the member
   * runs `sudo aether update` in a terminal. Absent when the probe failed.
   */
  install_method?: 'direct' | 'admin-prompt' | 'manual'
}

/** update.apply: what the self-update replaced and what happens next. */
export interface UpdateApplyResult {
  /** Every binary path replaced, in order. */
  updated: string[]
  version: string
  /** True only under the desktop shell, which respawns the gateway. */
  restarting: boolean
  note?: string
  /**
   * Present when a co-located aether-server was replaced too: the running
   * server keeps the old code until this command restarts its unit.
   */
  restart_command?: string
  /**
   * True when the gateway started a desktop-app rebuild in the background
   * after swapping the CLI binary.
   */
  rebuilding: boolean
}

/** update.status: progress of a desktop-app rebuild running in this gateway
 * process. */
export interface UpdateBuildStatus {
  phase:
    | 'idle'
    | 'unpacking'
    | 'fetching node'
    | 'installing dependencies'
    | 'packaging'
    | 'installing'
    | 'done'
    | 'error'
  /** Up to the last 20 lines of the build's own output. */
  lines_tail?: string[]
  /** The real build error; present only when phase is "error". */
  error?: string
}

// The server's own update, from internal/protocol/serverupdate.go and the
// server.update event payload in internal/events/serverupdate.go. Calling
// server.update is admin only; reading the status is not, so a member who
// cannot press the button can still be told why the server is restarting.

/** One update recorded and waiting for an idle server. */
export interface PendingServerUpdate {
  version: string
  /** The member id that asked for it. */
  requested_by: string
  requested_at: string
}

/** What a pending update is still waiting for. Paused runs are reported
 * but hold nothing back: a frozen container survives a restart. */
export interface ServerUpdateWaiting {
  runs: number
  paused: number
  shells: number
}

/** The outcome of the last update the server tried. */
export interface ServerUpdateAttempt {
  version: string
  outcome: 'applied' | 'failed'
  /** The real error behind a failed attempt. */
  detail?: string
  at: string
}

/**
 * server.update_status: whether this server can replace its own binaries,
 * and what update is in flight. `capable` is false on the documented
 * unprivileged install - the binary directory is not writable by the
 * service user - and `manual_commands` then carries what to run on the
 * server host instead.
 */
export interface ServerUpdateStatus {
  server_version: string
  latest?: string
  update_available: boolean
  capable: boolean
  /** Which reason the server cannot update itself. */
  incapable?: string
  pending?: PendingServerUpdate
  waiting?: ServerUpdateWaiting
  last?: ServerUpdateAttempt
  manual_commands?: string[]
}

/** When an update applies: immediately, at the next idle moment, or never
 * (which clears the pending one). */
export type ServerUpdateWhen = 'now' | 'idle' | 'cancel'

/** server.update: what the call recorded. The version fields are empty for
 * a cancel. */
export interface ServerUpdateResult {
  status: 'applying' | 'scheduled' | 'cancelled'
  version?: string
  requested_by?: string
  requested_at?: string
}

/** How far one update has got. `restarting` is the last frame the socket
 * carries: the server re-executes there and every connection drops. */
export type ServerUpdatePhase =
  | 'scheduled'
  | 'applying'
  | 'restarting'
  | 'failed'
  | 'cancelled'

/** The server.update event payload: one moment of a self-update. */
export interface ServerUpdatePayload {
  phase: ServerUpdatePhase
  version?: string
  actor_id?: string
  /** The real error behind a failed phase. */
  detail?: string
}


/** env.harnesses: one setup-capable harness's local availability. */
export interface HarnessStatus {
  name: string
  installed: boolean
}

/** The env.harnesses verb result: the setup-capable harnesses plus, when
 * the saved link config knows exactly one repository folder, a prefill
 * suggestion for the wizard's from-repo input. */
export interface EnvHarnessesResult {
  harnesses: HarnessStatus[]
  /** The folders the gateway looked in, so an empty result can say where. */
  searched: string[]
  /** Why the login shell could not be asked for its PATH; set only when
   * the probe failed and just the standard folders were checked. */
  warning?: string
  repo_path?: string
}

