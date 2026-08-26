import type { Api } from '@/lib/api'
import type {
  AgentInfo,
  Approval,
  BudgetReport,
  Member,
  Run,
  Schedule,
  ServerInfo,
  Session,
  Template,
  ToolSnapshot,
  Workspace,
} from '@/lib/types'

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

export const session: Session = {
  id: 'ses_1',
  workspace_id: 'wsp_1',
  name: 'checkout rewrite',
  base_branch: 'main',
  created_at: '2026-08-14T10:00:00Z',
}

export const otherSession: Session = {
  id: 'ses_2',
  workspace_id: 'wsp_1',
  name: 'flaky tests',
  base_branch: 'main',
  created_at: '2026-08-14T09:00:00Z',
}

export function run(over: Partial<Run> = {}): Run {
  return {
    id: 'run_1',
    session_id: session.id,
    member_id: alice.id,
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
  dashboard_port: 8080,
  member: alice,
  disk: {
    used_bytes: 512 * 1024 * 1024,
    total_bytes: 2 * 1024 * 1024 * 1024,
    free_bytes: 1536 * 1024 * 1024,
    worktree_bytes: 256 * 1024 * 1024,
    transcript_bytes: 128 * 1024 * 1024,
    database_bytes: 64 * 1024 * 1024,
  },
}

export function approval(over: Partial<Approval> = {}): Approval {
  return {
    id: 'apr_1',
    session_id: session.id,
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
  session_id: session.id,
  name: 'nightly triage',
  task: 'triage the flaky tests',
  harness: 'claude',
  mode: 'headless',
  created_at: '2026-08-14T09:00:00Z',
}

export const workspace: Workspace = {
  id: 'wsp_1',
  name: 'main-repo',
  created_at: '2026-08-14T08:00:00Z',
}

export function schedule(over: Partial<Schedule> = {}): Schedule {
  return {
    id: 'sch_1',
    session_id: session.id,
    template: template.name,
    cron: '0 3 * * *',
    member_id: alice.id,
    created_at: '2026-08-14T09:30:00Z',
    next_fire_at: '2026-08-15T03:00:00Z',
    ...over,
  }
}

export function toolSnapshot(over: Partial<ToolSnapshot> = {}): ToolSnapshot {
  return {
    id: 'tsn_1',
    workspace_id: workspace.id,
    member_id: alice.id,
    digest: 'sha256:abcd1234',
    manifest: { executable: 'claude', version: '1.0.0' },
    created_at: '2026-08-14T08:30:00Z',
    active: true,
    ...over,
  }
}

export function agentInfo(over: Partial<AgentInfo> = {}): AgentInfo {
  return { name: 'claude', source: 'shipped', ...over }
}

export function budget(
  sessionID: string,
  over: Partial<BudgetReport> = {},
): BudgetReport {
  return {
    session_id: sessionID,
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

/** An Api stub; every method is a spy so tests can assert on calls. */
export function fakeApi(over: Partial<Api> = {}): Api {
  return {
    serverInfo: vi.fn(async () => serverInfo),
    sessionList: vi.fn(async () => [session, otherSession]),
    memberList: vi.fn(async () => [alice, bob]),
    runList: vi.fn(async () => [run()]),
    runGet: vi.fn(async () => run()),
    runLaunch: vi.fn(async () => run()),
    runKill: vi.fn(async () => ({})),
    runPause: vi.fn(async () => ({})),
    runResume: vi.fn(async () => ({})),
    runInject: vi.fn(async () => ({})),
    runClose: vi.fn(async () => run({ status: 'merged' })),
    runHandoff: vi.fn(async () => ({})),
    approvalList: vi.fn(async () => []),
    approvalDecide: vi.fn(async () => approval()),
    presenceRoster: vi.fn(async () => []),
    presenceHeartbeat: vi.fn(async () => 90),
    budgetGet: vi.fn(async (sessionID: string) => budget(sessionID)),
    templateList: vi.fn(async () => [template]),
    templateLaunch: vi.fn(async () => ({
      run: run({ id: 'run_tpl' }),
      base_branch: 'main',
    })),
    sessionTimeline: vi.fn(async () => ({ events: [], next_seq: 0, more: false })),
    runOverlaps: vi.fn(async () => []),
    runPatch: vi.fn(async (runID: string) => ({
      run_id: runID,
      base: 'basesha0',
      patch: '',
      truncated: false,
    })),
    disk: vi.fn(async () => ({
      used_bytes: 512 * 1024 * 1024,
      total_bytes: 2 * 1024 * 1024 * 1024,
      free_bytes: 1536 * 1024 * 1024,
      worktree_bytes: 256 * 1024 * 1024,
      transcript_bytes: 128 * 1024 * 1024,
      database_bytes: 64 * 1024 * 1024,
    })),
    capabilities: vi.fn(async () => ({
      gateway: 'remote',
      methods: ['*'],
      ws: ['events', 'attach'],
    })),
    eventsSocket: vi.fn(() => 'ws://localhost/ws/events'),
    attachSocket: vi.fn((runID: string) => `ws://localhost/ws/attach/${runID}`),
    shellSocket: vi.fn(() => 'ws://localhost/ws/shell'),
    memberInvite: vi.fn(async () => ({
      code: 'inv-code-1',
      expires_at: '2026-08-15T10:00:00Z',
    })),
    memberApprove: vi.fn(async () => bob),
    memberRemove: vi.fn(async () => ({})),
    memberColor: vi.fn(async () => alice),
    memberRole: vi.fn(async () => bob),
    workspaceAdd: vi.fn(async () => workspace),
    workspaceListFull: vi.fn(async () => [workspace]),
    sessionNew: vi.fn(async () => session),
    sessionSettings: vi.fn(async () => session),
    toolsList: vi.fn(async () => [toolSnapshot()]),
    toolsVerify: vi.fn(async () => ({ verified: true })),
    toolsRollback: vi.fn(async () => ({})),
    toolsReset: vi.fn(async () => ({ reset: true })),
    budgetSet: vi.fn(async () => budget(session.id)),
    templateSave: vi.fn(async () => template),
    templateDelete: vi.fn(async () => ({})),
    scheduleList: vi.fn(async () => [schedule()]),
    scheduleSave: vi.fn(async () => schedule()),
    scheduleDelete: vi.fn(async () => ({})),
    profileStatus: vi.fn(async () => ({
      snapshot: {
        id: 'psn_1',
        harness: 'claude',
        digest: 'sha256:beef5678',
        created_at: '2026-08-14T08:00:00Z',
      },
      snapshots: [
        {
          id: 'psn_1',
          harness: 'claude',
          digest: 'sha256:beef5678',
          created_at: '2026-08-14T08:00:00Z',
        },
      ],
    })),
    profileRollback: vi.fn(async () => ({})),
    agentList: vi.fn(async () => [
      agentInfo(),
      agentInfo({ name: 'myagent', source: 'member' }),
    ]),
    agentRegister: vi.fn(async () => ({})),
    runProtect: vi.fn(async () => ({})),
    runRelaunch: vi.fn(async () => run({ id: 'run_2' })),
    localLinkStatus: vi.fn(async () => ({
      linked: true,
      addr: 'host:2222',
      user: 'alice',
      repo: '/src/repo',
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
    localDaemonInstall: vi.fn(async () => ({
      unit_path: '/home/alice/.config/systemd/user/aether-sync.service',
      note: 'enable with systemctl --user enable --now aether-sync',
    })),
    localDaemonStatus: vi.fn(async () => ({
      installed: false,
      unit_path: '',
    })),
    localImageScaffold: vi.fn(async () => ({ written: ['Dockerfile'] })),
    ...over,
  }
}
