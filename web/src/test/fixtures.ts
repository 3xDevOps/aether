import type { Api } from '@/lib/api'
import type {
  AgentInfo,
  Approval,
  BudgetReport,
  EvidencePacket,
  Member,
  Mission,
  MissionAttempt,
  MissionPlanItem,
  MissionPlanReview,
  MissionQuestion,
  MissionTask,
  MissionTaskRevision,
  RoomMessage,
  RoomStatusResult,
  Run,
  Schedule,
  ServerInfo,
  ServerUpdateStatus,
  Template,
  UpdateStatus,
  UsageResult,
  Workspace,
} from '@/lib/types'
import type { Candidate } from '@/lib/integration-types'
export const alice: Member = {
  id: 'mem_alice',
  display_name: 'Alice',
  color: '#e6194b',
  role: 'admin',
}

export const bob: Member = {
  id: 'mem_bob',
  display_name: 'Bob',
  color: '#3cb44b',
  role: 'collaborator',
}

export const vera: Member = {
  id: 'mem_vera',
  display_name: 'Vera',
  color: '#f58231',
  role: 'viewer',
}

export const workspace: Workspace = {
  id: 'wsp_1',
  name: 'main-repo',
  base_branch: 'main',
  created_at: '2026-08-14T08:00:00Z',
}

export const integrationCandidate: Candidate = {
  candidate_id: 'candidate_fixture',
  workspace_id: workspace.id,
  submissions: [],
  inputs: [],
  target_ref: 'refs/heads/main',
  expected_target_revision: 'fixture-target-revision',
  state: 'preparing',
  applied_inputs: 0,
  verifications: [],
  mutations: [],
  created_at: '2026-08-14T10:05:00Z',
  expires_at: '2026-09-14T10:05:00Z',
  version: 1,
}

export const otherWorkspace: Workspace = {
  id: 'wsp_2',
  name: 'docs-site',
  base_branch: 'main',
  created_at: '2026-08-14T07:00:00Z',
}
export function mission(over: Partial<Mission> = {}): Mission {
  return {
    id: 'mission_1',
    workspace_id: workspace.id,
    objective: 'coordinate checkout work',
    accountable_human_id: alice.id,
    integrator: { account_member_id: alice.id, harness: 'claude', mode: 'headless' },
    execution_choices: [{ account_member_id: alice.id, harness: 'claude', mode: 'headless' }],
    max_concurrent_attempts: 2,
    max_total_attempts: 8,
    current_integrator_run_id: 'run_integrator',
    integrator_generation: 1,
    accepted_set_version: 0,
    phase: 'active',
    plan_version: 1,
    open_questions: 0,
    created_at: '2026-08-14T10:00:00Z',
    updated_at: '2026-08-14T10:00:00Z',
    ...over,
  }
}

export function missionQuestion(over: Partial<MissionQuestion> = {}): MissionQuestion {
  return {
    id: 'question_1',
    mission_id: 'mission_1',
    seq: 1,
    body: 'which checkout flow?',
    asked_by_run_id: 'run_integrator',
    asked_at: '2026-08-14T10:01:00Z',
    ...over,
  }
}

export function missionPlanReview(over: Partial<MissionPlanReview> = {}): MissionPlanReview {
  return {
    mission_id: 'mission_1',
    plan_version: 1,
    summary: 'split the checkout rewrite into two bounded tasks',
    submitted_by_run_id: 'run_integrator',
    submitted_at: '2026-08-14T10:02:00Z',
    submitted_phase: 'clarified',
    ...over,
  }
}

export function missionPlanItem(over: Partial<MissionPlanItem> = {}): MissionPlanItem {
  return {
    task_id: 'task_1',
    revision: 2,
    new_task: false,
    material: false,
    title: 'rewrite the guest checkout flow',
    ...over,
  }
}

export function missionTaskRevision(over: Partial<MissionTaskRevision> = {}): MissionTaskRevision {
  return {
    task_id: 'task_1',
    revision: 1,
    title: 'rewrite the guest checkout flow',
    objective: 'replace the legacy guest checkout controller',
    scope: { expected_paths: ['web/checkout/'] },
    evidence_requirements: [],
    status: 'accepted',
    created_at: '2026-08-14T10:02:00Z',
    ...over,
  }
}

export function missionTask(over: Partial<MissionTask> = {}): MissionTask {
  return {
    id: 'task_1',
    mission_id: 'mission_1',
    current_revision: 1,
    revision: missionTaskRevision(),
    status: 'ready',
    created_at: '2026-08-14T10:02:00Z',
    updated_at: '2026-08-14T10:02:00Z',
    ...over,
  }
}

export function missionAttempt(over: Partial<MissionAttempt> = {}): MissionAttempt {
  return {
    id: 'attempt_1',
    mission_id: 'mission_1',
    task_id: 'task_1',
    task_revision: 1,
    number: 1,
    dispatch_key: 'dispatch_1',
    harness: 'claude',
    mode: 'headless',
    state: 'running',
    run_id: 'run_worker',
    authority_generation: 1,
    integrator_generation: 1,
    created_at: '2026-08-14T10:03:00Z',
    reserved_at: '2026-08-14T10:03:00Z',
    ...over,
  }
}

export function run(over: Partial<Run> = {}): Run {
  return {
    id: 'run_1',
    workspace_id: workspace.id,
    member_id: alice.id,
    account_member_id: alice.id,
    task: 'rewrite the checkout flow',
    harness: 'claude',
    mode: 'tui',
    status: 'running',
    branch: 'aether/run-1-checkout',
    created_at: '2026-08-14T10:01:00Z',
    started_at: '2026-08-14T10:02:00Z',
    finished_at: null,
    ...over,
  }
}

export const serverInfo: ServerInfo = {
  server_version: '1.2.3',
  protocol_version: '1',
  time: '2026-08-14T10:05:00Z',
  member: alice,
  disk: {
    used_bytes: 512 * 1024 * 1024,
    total_bytes: 2 * 1024 * 1024 * 1024,
    free_bytes: 1536 * 1024 * 1024,
    worktree_bytes: 256 * 1024 * 1024,
    transcript_bytes: 128 * 1024 * 1024,
    database_bytes: 64 * 1024 * 1024,
    repo_bytes: 512 * 1024 * 1024,
  },
}

export function approval(over: Partial<Approval> = {}): Approval {
  return {
    id: 'apr_1',
    workspace_id: workspace.id,
    run_id: 'run_1',
    action: 'write src/checkout.ts',
    detail: 'the agent wants to edit a file outside its allowlist',
    decision: 'requested',
    created_at: '2026-08-14T10:03:00Z',
    ...over,
  }
}

export const template: Template = {
  id: 'tpl_1',
  workspace_id: workspace.id,
  name: 'nightly triage',
  task: 'triage the flaky tests',
  harness: 'claude',
  mode: 'headless',
  created_at: '2026-08-14T09:00:00Z',
}

export function schedule(over: Partial<Schedule> = {}): Schedule {
  return {
    id: 'sch_1',
    workspace_id: workspace.id,
    template: template.name,
    cron: '0 3 * * *',
    member_id: alice.id,
    created_at: '2026-08-14T09:30:00Z',
    next_fire_at: '2026-08-15T03:00:00Z',
    ...over,
  }
}


export function agentInfo(over: Partial<AgentInfo> = {}): AgentInfo {
  return {
    name: 'claude',
    source: 'shipped',
    installed: true,
    install_script: 'curl -fsSL https://claude.ai/install.sh | bash',
    ...over,
  }
}

export function budget(
  workspaceID: string,
  over: Partial<BudgetReport> = {},
): BudgetReport {
  return {
    workspace_id: workspaceID,
    state: 'ok',
    spend: {
      runs: 1,
      metered_runs: 1,
      unmetered_runs: 0,
      input_tokens: 1000,
      output_tokens: 200,
      cost_usd: 0.5,
    },
    ...over,
  }
}


/** One update.check answer: a CLI a release behind, a current server. */
export function updateStatus(over: Partial<UpdateStatus> = {}): UpdateStatus {
  return {
    cli: {
      version: 'v1.2.3',
      commit: 'abc1234',
      latest: 'v1.3.0',
      update_available: true,
      asset: 'aether-linux-amd64',
      release_url: 'https://github.com/3xDevOps/Aether/releases/tag/v1.3.0',
      dev: false,
      disabled: false,
      can_self_update: true,
      checked_at: '2026-08-14T10:00:00Z',
    },
    server_version: 'v1.3.0',
    server_behind: false,
    supervised: true,
    cli_path: '/home/user/.local/bin/aether',
    install_method: 'direct',
    ...over,
  }
}

/** One server.update_status answer: a current server that could replace
 * its own binaries if it had to. The banner tests override it. */
export function serverUpdateStatus(
  over: Partial<ServerUpdateStatus> = {},
): ServerUpdateStatus {
  return {
    server_version: 'v1.2.3',
    latest: 'v1.3.0',
    update_available: false,
    capable: true,
    ...over,
  }
}
export function roomMessage(over: Partial<RoomMessage> = {}): RoomMessage {
  return {
    id: 'message_1',
    workspace_id: workspace.id,
    run_id: 'run_1',
    actor_id: alice.id,
    kind: 'comment',
    body: 'hello',
    state: 'sent',
    created_at: '2026-08-14T10:00:00Z',
    updated_at: '2026-08-14T10:00:00Z',
    ...over,
  }
}

export function evidencePacket(over: Partial<EvidencePacket> = {}): EvidencePacket {
  return {
    id: 'packet_1',
    workspace_id: workspace.id,
    run_id: 'run_1',
    creator_id: alice.id,
    trigger: 'report',
    objective: 'Inspect the change',
    captured_at: '2026-08-14T10:00:00Z',
    event_boundary: 1,
    created_at: '2026-08-14T10:00:00Z',
    updated_at: '2026-08-14T10:00:00Z',
    ...over,
  }
}


/** An Api stub; every method is a spy so tests can assert on calls. */
export function fakeApi(over: Partial<Api> = {}): Api {
  return {
    serverInfo: vi.fn(async () => serverInfo),
    workspaceGet: vi.fn(async () => workspace),
    memberList: vi.fn(async () => [alice, bob]),
    accountUsage: vi.fn(async (): Promise<UsageResult> => ({
      account_member_id: alice.id,
      providers: [],
    })),
    accountList: vi.fn(async () => ({ accounts: [alice], shared_with: [] })),
    accountShare: vi.fn(async () => ({})),
    accountRevoke: vi.fn(async () => ({})),
    runList: vi.fn(async () => [run()]),
    runGet: vi.fn(async () => run()),
    missionCreate: vi.fn(async () => ({ mission: mission() })),
    missionShow: vi.fn(async () => ({ mission: mission(), tasks: [], attempts: [], submissions: [], diagnostics: [], questions: [], plan_reviews: [] })),
    missionList: vi.fn(async () => ({ missions: [mission()], next_cursor: undefined })),
    missionQuestionAnswer: vi.fn(async () => ({ question: missionQuestion({ answer: 'the guest flow', answered_by_member_id: alice.id, answered_at: '2026-08-14T10:03:00Z' }) })),
    missionPlanDecide: vi.fn(async () => ({ mission: mission() })),
    missionCancel: vi.fn(async () => ({ mission: mission({ phase: 'rejected' }) })),
    missionWorkerRelease: vi.fn(async () => ({ run_id: 'run_worker', takeover_active: false, takeover_generation: 2 })),
    missionReplaceIntegrator: vi.fn(async () => ({ mission: mission(), run_id: 'run_integrator' })),
    runLaunch: vi.fn(async () => run()),
    runKill: vi.fn(async () => ({})),
    runDelete: vi.fn(async () => ({})),
    runPause: vi.fn(async () => ({})),
    runResume: vi.fn(async () => ({})),
    runInject: vi.fn(async () => ({ message: roomMessage({ kind: 'steer_request', state: 'queued' }) })),
    runClose: vi.fn(async () => run({ status: 'merged' })),
    runHandoff: vi.fn(async () => ({})),
    runRoomList: vi.fn(async () => ({ messages: [] })),
    runRoomStatus: vi.fn(async (): Promise<RoomStatusResult> => ({
      workspace_id: workspace.id,
      run_id: 'run_1',
      protected: false,
      watchers: [],
      queued_steers: 0,
    })),
    runRoomPost: vi.fn(async () => ({ message: roomMessage() })),
    runRoomDecide: vi.fn(async () => ({ message: roomMessage({ state: 'denied' }) })),
    runEvidenceList: vi.fn(async () => ({ packets: [] })),
    runEvidenceGet: vi.fn(async () => ({ packet: evidencePacket() })),
    runEvidencePatch: vi.fn(async () => ({ packet: evidencePacket(), patch: '', truncated: false })),
    runEvidenceTranscript: vi.fn(async () => ({ packet: evidencePacket(), data_base64: '', truncated: false })),
    integrationPrepare: vi.fn(async () => ({ candidate: integrationCandidate })),
    integrationShow: vi.fn(async () => ({ candidate: integrationCandidate })),
    integrationList: vi.fn(async () => ({ candidates: [] })),
    integrationResolve: vi.fn(async () => ({ candidate: integrationCandidate })),
    integrationVerify: vi.fn(async () => ({ candidate: integrationCandidate })),
    integrationRequestDelivery: vi.fn(async () => ({ candidate: integrationCandidate })),
    integrationDecide: vi.fn(async () => ({ candidate: integrationCandidate })),
    integrationDeliver: vi.fn(async () => ({ candidate: integrationCandidate })),
    integrationPatch: vi.fn(async () => ({ patch: '', truncated: false })),
    integrationDelete: vi.fn(async () => ({})),
    approvalList: vi.fn(async () => []),
    approvalDecide: vi.fn(async () => approval()),
    presenceRoster: vi.fn(async () => []),
    presenceHeartbeat: vi.fn(async () => 90),
    budgetGet: vi.fn(async (workspaceID: string) => budget(workspaceID)),
    templateList: vi.fn(async () => [template]),
    templateLaunch: vi.fn(async () => ({
      run: run({ id: 'run_tpl' }),
      base_commit: '1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b',
      base_branch: 'main',
      base_source: 'local',
      base_checked_at: '2026-08-14T10:05:00Z',
    })),
    workspaceTimeline: vi.fn(async () => ({
      events: [],
      next_seq: 0,
      more: false,
    })),
    runOverlaps: vi.fn(async () => []),
    runPatch: vi.fn(async (runID: string) => ({
      run_id: runID,
      base: 'basesha0',
      patch: '',
      truncated: false,
    })),
    filesTree: vi.fn(async () => ({ entries: [] })),
    filesRead: vi.fn(async () => ({
      content: '', truncated: false, binary: false, size: 0, revision: '', writable: true,
    })),
    filesWrite: vi.fn(async ({ content }) => ({
      content, truncated: false, binary: false,
      size: new TextEncoder().encode(content).length, revision: 'saved', writable: true,
    })),
    filesDiff: vi.fn(async () => ({ patch: '', truncated: false })),
    disk: vi.fn(async () => ({
      used_bytes: 512 * 1024 * 1024,
      total_bytes: 2 * 1024 * 1024 * 1024,
      free_bytes: 1536 * 1024 * 1024,
      worktree_bytes: 256 * 1024 * 1024,
      transcript_bytes: 128 * 1024 * 1024,
      database_bytes: 64 * 1024 * 1024,
      repo_bytes: 512 * 1024 * 1024,
    })),
    capabilities: vi.fn(async () => ({
      gateway: 'remote',
      methods: ['*'],
      ws: ['events', 'attach', 'terminal'],
    })),
    eventsSocket: vi.fn(() => 'ws://localhost/ws/events'),
    attachSocket: vi.fn((runID: string) => `ws://localhost/ws/attach/${runID}`),
    attachShellSocket: vi.fn(
      (runID: string, tab: string) =>
        `ws://localhost/ws/attach/${runID}?shell=${encodeURIComponent(tab)}`,
    ),
    terminalStatus: vi.fn(async () => ({ running: false, tabs: [] })),
    terminalHistory: vi.fn(async () => ({ lines: [], has_more: false })),
    uploadTerminalImage: vi.fn(async () => ({ path: '/home/alice/.aether/uploads/image.png' })),
    terminalStop: vi.fn(async () => ({})),
    envSave: vi.fn(async () => ({ image: 'aether/member-1:123' })),
    envReset: vi.fn(async () => ({})),
    githubConnect: vi.fn(async () => ({
      login: 'octocat',
      signing_key: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI aether mbr_1',
      fingerprint: 'SHA256:9wPnHRtG0DPQNo8VYbC2mSczRRRUYY7NoLgTHTAlYFA',
    })),
    githubProbe: vi.fn(async () => ({
      status: 'ok' as const,
      version: '2.100.0',
      minimum: '2.81.0',
      detail: 'gh version 2.100.0 (2026-09-03)',
      image: 'ghcr.io/3xdevops/aether-standard:latest',
    })),
    terminalSocket: vi.fn(
      (tab: string) => `ws://localhost/ws/terminal?tab=${encodeURIComponent(tab)}`,
    ),
    memberInvite: vi.fn(async () => ({
      code: 'inv-code-1',
      expires_at: '2026-08-15T10:00:00Z',
    })),
    memberApprove: vi.fn(async () => bob),
    memberRemove: vi.fn(async () => ({})),
    memberColor: vi.fn(async () => alice),
    memberGit: vi.fn(async (name: string, email: string) => ({
      ...alice,
      git_name: name,
      git_email: email,
    })),
    memberRole: vi.fn(async () => bob),
    workspaceAdd: vi.fn(async () => workspace),
    workspaceListFull: vi.fn(async () => [workspace, otherWorkspace]),
    workspaceSettings: vi.fn(async () => workspace),
    // Local-only is the default in the mirror control plane; tests that
    // exercise configuration override the relevant response.
    workspaceMirrorStatus: vi.fn(async () => ({ enabled: false })),
    workspaceMirrorConfigure: vi.fn(async () => ({ enabled: false })),
    workspaceMirrorRefresh: vi.fn(async () => ({ enabled: false })),
    workspaceMirrorAdopt: vi.fn(async () => ({ enabled: false })),
    workspaceMirrorDisable: vi.fn(async () => ({ enabled: false })),
    budgetSet: vi.fn(async () => budget(workspace.id)),
    templateSave: vi.fn(async () => template),
    templateDelete: vi.fn(async () => ({})),
    scheduleList: vi.fn(async () => [schedule()]),
    scheduleSave: vi.fn(async () => schedule()),
    scheduleDelete: vi.fn(async () => ({})),
    configRoots: vi.fn(async () => ({
      roots: [
        {
          harness: 'claude',
          path: '~/.claude',
          runtime_ignores: ['projects/'],
        },
        {
          harness: 'codex',
          path: '~/.codex',
          runtime_ignores: ['tmp/'],
        },
        {
          harness: 'pi',
          path: '~/.pi',
          runtime_ignores: ['agent/sessions/'],
        },
      ],
    })),
    configTree: vi.fn(async () => ({ entries: [] })),
    configRead: vi.fn(async () => ({
      content: '', size: 0, binary: false, truncated: false, revision: '', writable: true,
    })),
    configWrite: vi.fn(async ({ content }) => ({
      content, size: new TextEncoder().encode(content).length,
      binary: false, truncated: false, revision: 'saved', writable: true,
    })),
    configImport: vi.fn(async ({ harness, files }) => ({
      harness, files: files.length, bytes: 0, excluded: [],
    })),
    agentList: vi.fn(async () => [
      agentInfo(),
      agentInfo({ name: 'myagent', source: 'member', install_script: undefined }),
    ]),
    agentRegister: vi.fn(async () => ({})),
    runProtect: vi.fn(async () => ({})),
    runArchive: vi.fn(async (runID: string, archived: boolean) =>
      run({
        id: runID,
        archived_at: archived ? '2026-08-14T10:00:00Z' : undefined,
        deletes_at: archived ? '2026-08-28T10:00:00Z' : undefined,
      }),
    ),
    runRelaunch: vi.fn(async () => run({ id: 'run_2' })),
    localLinkStatus: vi.fn(async () => ({
      server_configured: true,
      linked: true,
      addr: 'host:2222',
      user: 'alice',
      repo: '/src/repo',
    })),
    // The machine's own git config, which the wizard offers as the
    // default identity.
    localGitIdentity: vi.fn(async () => ({
      name: 'Alice Local',
      email: 'alice@example.invalid',
    })),
    localLinkApply: vi.fn(async () => ({
      addr: 'host:2222',
      user: 'aether',
      member: { id: alice.id, display_name: alice.display_name, role: 'admin' },
    })),
    localLinkRepo: vi.fn(async () => ({
      repo: '/src/repo',
      remote: 'aether',
      url: 'ssh://alice@host:2222/wsp_1',
    })),
    // Mirrors the gateway: link.switch always refuses with the restart
    // instruction; the SSH identity is process-lifetime.
    localLinkSwitch: vi.fn(async (name: string) => {
      throw new Error(`restart aether gui --server ${name} to switch servers`)
    }),
    localPull: vi.fn(async () => ({
      branch: 'aether/run-1-checkout',
      ref: 'refs/heads/aether/run-1-checkout',
      output: '',
      current: false,
      dirty: false,
    })),
    localPullSwitch: vi.fn(async (runID: string) => ({
      branch: runID,
    })),
    localRepoFastForward: vi.fn(async () => ({
      branch: 'main',
      commit: '1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b',
      current: true,
      dirty: false,
      output: 'From ssh://alice@host:2222/wsp_1\n * branch main -> FETCH_HEAD',
    })),
    localRepoPush: vi.fn(async () => ({
      branch: 'main',
      remote: 'aether',
      state: 'pushed' as const,
      local_commit: '9f1c2ab3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9',
      workspace_commit: '',
      ahead: 1,
      behind: 0,
      output:
        'To ssh://alice@host:2222/wsp_1\n * [new branch] main -> main',
    })),
    localSyncStart: vi.fn(async (runID: string) => ({
      run_id: runID,
      state: 'running',
    })),
    localSyncStop: vi.fn(async (runID: string) => ({
      run_id: runID,
      state: 'stopped',
    })),
    localSyncStatus: vi.fn(async () => ({ sessions: [] })),
    localForwardStart: vi.fn(async (target: string, port: number) => ({
      target,
      port,
      local_port: port,
      state: 'active' as const,
    })),
    localForwardStop: vi.fn(async (target: string, port: number) => ({
      target,
      port,
      state: 'stopped' as const,
    })),
    localForwardStatus: vi.fn(async () => ({ forwards: [] })),
    localDaemonInstall: vi.fn(async () => ({
      unit_path: '/home/alice/.config/systemd/user/aether-sync.service',
      note: 'enable with systemctl --user enable --now aether-sync',
    })),
    localDaemonStatus: vi.fn(async () => ({
      installed: false,
      unit_path: '',
    })),
    localUpdateCheck: vi.fn(async () => updateStatus()),
    localUpdateApply: vi.fn(async () => ({
      updated: ['/usr/local/bin/aether'],
      version: 'v1.3.0',
      restarting: true,
      rebuilding: false,
    })),
    localUpdateStatus: vi.fn(async () => ({ phase: 'idle' as const })),
    serverUpdateStatus: vi.fn(async () => serverUpdateStatus()),
    serverUpdate: vi.fn(async () => ({
      status: 'scheduled' as const,
      version: 'v1.3.0',
      requested_by: alice.id,
      requested_at: '2026-08-14T10:06:00Z',
    })),
    // The gateway knows one linked repo, so the verb suggests its folder
    // for the wizard's from-repo input.
    envHarnesses: vi.fn(async () => ({
      harnesses: [
        { name: 'claude', installed: true },
        { name: 'codex', installed: false },
        { name: 'pi', installed: false },
      ],
      searched: ['/usr/local/bin', '/home/alice/.local/bin'],
      repo_path: '/src/repo',
    })),
    ...over,
  }
}
