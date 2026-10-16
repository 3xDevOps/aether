import { deviceNeeds, needsYou, needsYouConditions, waitingOn, type AttentionState, type NeedsYouID } from '@/lib/needs-you'
import { presentRun } from '@/lib/status'
import type { MissionAttempt, Run } from '@/lib/types'
import type { MissionDetail } from '@/store/missions'
import { toRecord, type RunRecord } from '@/store/runs'
import {
  alice,
  approval,
  bob,
  mission,
  missionQuestion,
  pendingDevice,
  roomMessage,
  run,
  runRecords,
  stateContext,
} from '@/test/fixtures'

const record = (over: Parameters<typeof run>[0] = {}): RunRecord => toRecord(run(over))

it.each(['tui', 'acp'] as const)('keeps actual requests ahead of a reported %s outcome', (mode) => {
  const parked = record({
    mode, acp: mode === 'acp', status: 'needs-attention', state: 'failed',
    reason: 'agent reported failure', outcome_unseen: true,
  })
  expect(needsYou(parked, stateContext())?.id).toBe('failed')
  expect(needsYou(parked, stateContext({
    approvalsByRun: { [parked.id]: [approval()] },
  }))?.id).toBe('permission')
  expect(needsYou({
    ...parked, pending_inputs: [{ id: 'question_1', session_id: 'session_1', kind: 'question' }],
  }, stateContext())?.id).toBe('question')
  const success = { ...parked, state: 'finished' as const, reason: 'agent reported success' }
  expect(needsYou(success, stateContext())).toBeUndefined()
  expect(presentRun(success, stateContext())).toEqual({ state: 'finished', reason: 'Agent reported success. Run remains open for follow-up.' })
})

const integrator = record({ id: 'run_integrator', mission_id: 'mission_1', mission_role: 'integrator' })
const worker = record({
  id: 'run_worker',
  mission_id: 'mission_1',
  mission_role: 'worker',
  integrator_run_id: integrator.id,
  mode: 'headless',
})

function detail(over: Partial<MissionDetail> = {}): MissionDetail {
  return {
    mission: mission(),
    tasks: [],
    attempts: [],
    submissions: [],
    diagnostics: [],
    questions: [],
    ...over,
  }
}

function attempt(over: Partial<MissionAttempt> = {}): MissionAttempt {
  return {
    id: 'attempt_1',
    mission_id: 'mission_1',
    task_id: 'task_1',
    task_revision: 1,
    number: 3,
    dispatch_key: 'dispatch_1',
    harness: 'claude',
    mode: 'headless',
    state: 'running',
    run_id: worker.id,
    authority_generation: 1,
    integrator_generation: 1,
    created_at: '2026-08-14T10:00:00Z',
    reserved_at: '2026-08-14T10:00:00Z',
    ...over,
  } as MissionAttempt
}

const rows: {
  id: NeedsYouID
  run: RunRecord
  ctx: Parameters<typeof stateContext>[0]
  reason: string
  state?: AttentionState
}[] = [
  {
    id: 'permission',
    run: record(),
    ctx: { approvalsByRun: { run_1: [approval({ action: 'run `go test ./...`' })] } },
    reason: 'Permission: run `go test ./...`',
  },
  {
    id: 'question',
    run: record({ pending_inputs: [{ id: 'request_1', session_id: 's', kind: 'question' }] }),
    ctx: {},
    reason: 'Question: answer in the terminal',
  },
  {
    id: 'queued-message',
    run: record({ member_id: bob.id }),
    ctx: {
      roomStatus: {
        run_1: {
          workspace_id: 'wsp_1',
          run_id: 'run_1',
          protected: false,
          watchers: [],
          queued_steers: 1,
          controller: { member_id: alice.id, connected: true, acquired_at: '2026-08-14T10:00:00Z' },
        },
      },
      roomMessages: {
        run_1: [roomMessage({ actor_id: bob.id, actor_display_name: 'Bob', kind: 'steer_request', state: 'queued' })],
      },
    },
    reason: 'Bob sent a message, approve to deliver',
  },
  {
    id: 'room-question',
    run: record(),
    ctx: { roomMessages: { run_1: [roomMessage({ actor_id: bob.id, kind: 'question', body: 'which port?' })] } },
    reason: 'Bob asked you: which port?',
  },
  {
    id: 'swarm-question',
    run: integrator,
    ctx: {
      missions: { mission_1: mission() },
      missionDetails: { mission_1: detail({ questions: [missionQuestion()] }) },
    },
    reason: 'The integrator asks: which checkout flow?',
  },
  {
    id: 'integrator-down',
    run: record({ ...integrator, status: 'failed' }),
    ctx: { missions: { mission_1: mission() } },
    reason: 'Integrator stopped, replace it to continue',
  },
  {
    id: 'control-hold',
    run: worker,
    ctx: {
      runs: runRecords(integrator),
      missionDetails: { mission_1: detail({ attempts: [attempt({ takeover_active: true, takeover_member_id: alice.id })] }) },
    },
    reason: 'You hold control of worker 3',
  },
  {
    id: 'disconnected',
    run: record({ status: 'needs-attention', state: 'disconnected', reason: 'enhanced session failed: acphost: initialize: EOF' }),
    ctx: {},
    reason: 'Enhanced could not start: acphost: initialize: EOF',
    state: 'disconnected',
  },
  {
    id: 'blocked',
    run: record({ ...worker, status: 'needs-attention', state: 'needs-you', reason: 'blocked: no database access' }),
    ctx: { runs: runRecords(integrator), missions: { mission_1: mission() } },
    reason: 'Worker blocked: no database access',
  },
  {
    id: 'needs-input',
    run: record({ mode: 'acp', acp: true, status: 'needs-attention', state: 'needs-you', reason: 'needs input: Which database?' }),
    ctx: {},
    reason: 'Agent asks: Which database?',
  },
  {
    id: 'failed',
    run: record({
      mode: 'acp', acp: true, status: 'needs-attention', state: 'failed',
      reason: 'enhanced turn failed: acphost: session/prompt: EOF', outcome_unseen: true,
    }),
    ctx: {},
    reason: 'Turn failed: acphost: session/prompt: EOF',
    state: 'failed',
  },
]

describe('needs you conditions', () => {
  it('has one test row per condition', () => {
    expect(rows.map((row) => row.id).sort()).toEqual(needsYouConditions.map((c) => c.id).sort())
  })

  it.each(rows)('$id needs the member who can resolve it', ({ id, run: r, ctx, reason, state = 'needs-you' }) => {
    const context = stateContext(ctx)
    const hit = needsYou(r, context)
    expect(hit?.id).toBe(id)
    expect(hit?.reason(r, context)).toBe(reason)
    expect(presentRun(r, context)).toMatchObject({ state, reason })
  })

  it('needs nobody for a plain working run', () => {
    expect(needsYou(record(), stateContext())).toBeUndefined()
    expect(presentRun(record({ status: 'queued' }), stateContext()).state).toBe('working')
  })

  it("needs the owner for a standalone run's blocked report", () => {
    const blocked = record({ status: 'needs-attention', state: 'needs-you', reason: 'blocked: need database credentials' })
    expect(presentRun(blocked, stateContext())).toMatchObject({
      state: 'needs-you',
      reason: 'Blocked: need database credentials',
    })
  })

  // The same cases as the attention table in docs/failure-handling.md.
  it.each([
    ['a request', { state: 'needs-you' }, 'needs-input'],
    ['a disconnected agent', { state: 'disconnected' }, 'disconnected'],
    ['an unseen failure', { state: 'failed', outcome_unseen: true }, 'failed'],
    ['a seen failure', { state: 'failed' }, undefined],
    ['an unseen success', { state: 'finished', outcome_unseen: true }, undefined],
    ['a working run', { status: 'running', state: 'working' }, undefined],
    ['an idle run', { state: 'idle' }, undefined],
    ['a run waiting on its agents', { state: 'waiting-agents' }, undefined],
    ['a run waiting on a tool', { state: 'waiting-tools' }, undefined],
    ['a paused request', { state: 'needs-you', paused: true }, undefined],
    ['a paused disconnect', { state: 'disconnected', paused: true }, undefined],
    ['a usage limit Aether resumes', { state: 'idle', reason: "Usage limit reached on Alice's account; moving to Bob's account" }, undefined],
    ['a usage limit Aether cannot resume', { state: 'needs-you', reason: "Usage limit reached on Alice's account; no fallback has usage left" }, 'needs-input'],
    ['a Background question', { mode: 'headless', state: 'needs-you', reason: 'needs input: Which port?' }, 'needs-input'],
    ['a Background request push does not announce', { mode: 'headless', state: 'needs-you' }, undefined],
    ['a Background disconnect', { mode: 'headless', state: 'disconnected' }, 'disconnected'],
    ['a Background failure', { mode: 'headless', state: 'failed', outcome_unseen: true }, undefined],
    ['a finished run with its outcome unseen', { status: 'failed', state: '', outcome_unseen: true }, undefined],
    ['a live run with no state', { state: '' }, undefined],
  ] satisfies [string, Partial<Run>, NeedsYouID | undefined][])('attention for %s: %s', (_, fields, want) => {
    expect(needsYou(record({ status: 'needs-attention', ...fields }), stateContext())?.id).toBe(want)
  })

  it.each([
    ['idle', 'agent idle', 'Agent idle'],
    ['idle', 'stalled: no output or file changes for 10m0s', 'No output or file changes for 10m0s'],
    ['idle', 'turn cancelled', 'Turn cancelled'],
    ['idle', 'turn ended: max_tokens', 'Stopped: the reply reached its token limit'],
    ['waiting-agents', 'waiting on 3 workers', 'waiting on 3 workers'],
    ['waiting-agents', 'working in background: 2 tasks', 'Working in background: 2 tasks'],
    ['waiting-tools', '', 'Waiting on a tool'],
  ] as const)('never needs anyone for a %s run parked with %j', (state, reason, shown) => {
    for (const acp of [false, true]) {
      const parked = record({ mode: acp ? 'acp' : 'tui', acp, status: 'needs-attention', state, reason: reason || undefined })
      expect(needsYou(parked, stateContext())).toBeUndefined()
      expect(waitingOn(parked, stateContext())).toBeUndefined()
      expect(presentRun(parked, stateContext())).toEqual({ state, reason: shown })
    }
  })

  it("labels a disconnected agent with the server's reason", () => {
    const parked = record({ status: 'needs-attention', state: 'disconnected', reason: "enhanced session ended: the agent's ACP server exited with code 1" })
    expect(presentRun(parked, stateContext())).toMatchObject({ state: 'disconnected', reason: "Agent disconnected: the agent's ACP server exited with code 1" })
    expect(presentRun({ ...parked, reason: 'agent exited 1' }, stateContext()).reason).toBe('Agent exited with code 1')
  })

  it.each(['agent idle', 'turn cancelled', 'turn interrupted', 'turn ended: refusal', 'needs input: Which database?'])(
    'does not need anyone while an Enhanced run is running after %s',
    (reason) => {
      const running = record({ mode: 'acp', acp: true, status: 'running', state: 'working', reason })
      expect(needsYou(running, stateContext())).toBeUndefined()
      expect(presentRun(running, stateContext()).state).toBe('working')
    },
  )

  it("does not need a member other than the owner for the agent's question", () => {
    const asking = record({ mode: 'acp', acp: true, status: 'needs-attention', state: 'needs-you', reason: 'needs input: Which database?' })
    const ctx = stateContext({ viewerID: bob.id, viewerRole: bob.role })
    expect(needsYou(asking, ctx)).toBeUndefined()
    expect(presentRun(asking, ctx)).toEqual({ state: 'idle', reason: 'Waiting for Alice' })
  })

  it("shows a paused run as Paused, not the agent's question", () => {
    const asking = record({ mode: 'acp', acp: true, status: 'needs-attention', state: 'needs-you', reason: 'needs input: Which database?' })
    expect(presentRun(asking, stateContext({ pausedRuns: { [asking.id]: true } }))).toEqual({ state: 'paused', reason: 'Paused' })
  })

  it.each([
    ['permission', 'Permission requested'],
    ['question', 'Question from the agent'],
  ] as const)('gives no terminal instruction for an enhanced run\'s %s', (kind, reason) => {
    const enhanced = record({ mode: 'acp', acp: true, pending_inputs: [{ id: 'r1', session_id: 's1', kind }] })
    expect(presentRun(enhanced, stateContext())).toMatchObject({ state: 'needs-you', reason })
  })

  it('treats a Background run the server drives over ACP as Enhanced', () => {
    const background = record({ mode: 'headless', acp: true, pending_inputs: [{ id: 'r1', session_id: 's1', kind: 'permission' }] })
    const shown = presentRun(background, stateContext())
    expect(shown.reason).toBe('Permission requested')
    expect(shown.needsYou?.action(background, undefined)).toEqual({ kind: 'open', label: 'Answer' })
  })

  it.each([
    [{ pending_inputs: [{ id: 'r1', session_id: 's1', kind: 'question' }] }, 'Open terminal'],
    [{ acp: true, pending_inputs: [{ id: 'r1', session_id: 's1', kind: 'question' }] }, 'Answer'],
    [{ status: 'needs-attention', state: 'needs-you', reason: 'needs input: Which port?' }, 'Reply'],
    [{ mode: 'headless', status: 'needs-attention', state: 'needs-you', reason: 'needs input: Which port?' }, 'Open'],
    [{ status: 'needs-attention', state: 'needs-you', reason: "Usage limit reached on Alice's account; no fallback has usage left" }, 'Open'],
    [{ status: 'needs-attention', state: 'failed', outcome_unseen: true }, 'Review'],
  ] satisfies [Partial<Run>, string][])('gives %o the primary action %s', (fields, label) => {
    const shown = record(fields)
    expect(needsYou(shown, stateContext())?.action(shown, undefined).label).toBe(label)
  })

  it('shows a request whose approval has not loaded as a plain Needs you', () => {
    const parked = record({ status: 'needs-attention', state: 'needs-you', reason: 'agent idle' })
    const shown = presentRun(parked, stateContext())
    expect(shown).toMatchObject({ state: 'needs-you', reason: 'Open the run to see its request' })
    expect(shown.needsYou?.action(parked, undefined)).toEqual({ kind: 'open', label: 'Open' })
  })
})

describe('viewer scoping', () => {
  const bobViewing = { viewerID: bob.id, viewerRole: bob.role }

  it("shows another member's waiting run as Idle, waiting for its owner", () => {
    const stopped = record({ status: 'needs-attention', state: 'needs-you', reason: 'needs input: Which port?' })
    const ctx = stateContext(bobViewing)
    expect(needsYou(stopped, ctx)).toBeUndefined()
    expect(waitingOn(stopped, ctx)).toBe(alice.id)
    expect(presentRun(stopped, ctx)).toEqual({ state: 'idle', reason: 'Waiting for Alice' })
  })

  it('names a waiting run by the member its owner was merged into', () => {
    const stopped = record({ status: 'needs-attention', state: 'needs-you', member_id: 'mem_old' })
    const ctx = stateContext({ ...bobViewing, mergedInto: { mem_old: alice.id } })
    expect(presentRun(stopped, ctx).reason).toBe('Waiting for Alice (merged)')
  })

  it("leaves another member's unreviewed finish Finished", () => {
    const finished = record({ status: 'completed', outcome_unseen: true })
    expect(presentRun(finished, stateContext(bobViewing))).toEqual({ state: 'finished', reason: 'Finished' })
  })

  it('needs the terminal controller for a permission on a run they hold', () => {
    const ctx = stateContext({
      ...bobViewing,
      approvalsByRun: { run_1: [approval()] },
      roomStatus: {
        run_1: {
          workspace_id: 'wsp_1',
          run_id: 'run_1',
          protected: false,
          watchers: [],
          queued_steers: 0,
          controller: { member_id: bob.id, connected: true, acquired_at: '2026-08-14T10:00:00Z' },
        },
      },
    })
    expect(needsYou(record(), ctx)?.id).toBe('permission')
  })

  it("shows a controller the requests on another member's run before the room loads", () => {
    const held = (pending: 'permission' | 'question', controller: string) =>
      record({
        mode: 'acp',
        controller_member_id: controller,
        pending_inputs: [{ id: 'req_1', session_id: 'sess_1', kind: pending }],
      })
    const ctx = stateContext(bobViewing)
    expect(needsYou(held('permission', bob.id), ctx)?.id).toBe('permission')
    expect(needsYou(held('question', bob.id), ctx)?.id).toBe('question')

    // The snapshot names the current holder over a room status read earlier.
    const stale = stateContext({
      ...bobViewing,
      roomStatus: {
        run_1: {
          workspace_id: 'wsp_1',
          run_id: 'run_1',
          protected: false,
          watchers: [],
          queued_steers: 0,
          controller: { member_id: bob.id, connected: true, acquired_at: '2026-08-14T10:00:00Z' },
        },
      },
    })
    expect(needsYou(held('permission', ''), stale)).toBeUndefined()
  })

  it("does not ask the owner to answer their own room question", () => {
    const ctx = stateContext({ roomMessages: { run_1: [roomMessage({ kind: 'question' })] } })
    expect(needsYou(record(), ctx)).toBeUndefined()
  })

  // The board card reads the server's count before the room loads; the run
  // header reads the loaded room. The server counts only questions to the owner.
  it.each([
    { asker: 'the owner', actor: alice.id, count: 0, want: undefined },
    { asker: 'a teammate', actor: bob.id, count: 1, want: 'room-question' },
  ])('agrees between card and header on a question from $asker', ({ actor, count, want }) => {
    const parked = { status: 'needs-attention' as const, state: 'idle' as const, reason: 'stalled: no output or file changes for 10m4s' }
    const card = needsYou(record({ ...parked, unanswered_questions: count }), stateContext())
    const header = needsYou(
      record({ ...parked, unanswered_questions: count }),
      stateContext({ roomMessages: { run_1: [roomMessage({ kind: 'question', actor_id: actor })] } }),
    )
    expect(card?.id).toBe(want)
    expect(header?.id).toBe(want)
  })

  it('lets an admin resolve a swarm question for another accountable human', () => {
    const ctx = {
      missions: { mission_1: mission({ accountable_human_id: bob.id, open_questions: 1 }) },
    }
    const theirs = record({ ...integrator, member_id: bob.id })
    expect(needsYou(theirs, stateContext(ctx))?.id).toBe('swarm-question')
    expect(needsYou(theirs, stateContext({ ...ctx, viewerID: 'mem_carol', viewerRole: 'collaborator' }))).toBeUndefined()
  })

  it("keeps a paused integrator's open swarm question", () => {
    const ctx = stateContext({
      missions: { mission_1: mission() },
      missionDetails: { mission_1: detail({ questions: [missionQuestion()] }) },
      pausedRuns: { [integrator.id]: true },
    })
    expect(needsYou(integrator, ctx)?.id).toBe('swarm-question')
    expect(needsYou(record({ ...integrator, status: 'needs-attention', state: 'needs-you', reason: 'needs input: Which flow?' }), {
      ...ctx, missionDetails: {},
    })).toBeUndefined()
  })

  it('needs nobody before the viewer is known', () => {
    const stopped = record({ status: 'needs-attention', state: 'needs-you' })
    expect(needsYou(stopped, stateContext({ viewerID: null, viewerRole: null }))).toBeUndefined()
    expect(presentRun(stopped, stateContext({ viewerID: null, viewerRole: null })).reason).not.toMatch(/^Waiting for/)
  })
})

describe('swarm workers', () => {
  const stoppedWorker = record({ ...worker, mode: 'tui', status: 'needs-attention', state: 'needs-you', reason: 'needs input: Which port?' })

  it('leaves a worker to its live integrator', () => {
    const ctx = stateContext({ runs: runRecords(integrator) })
    expect(needsYou(stoppedWorker, ctx)).toBeUndefined()
    expect(presentRun(stoppedWorker, ctx)).toEqual({ state: 'waiting-agents', reason: 'Waiting for the integrator' })
  })

  it('counts a worker once its integrator is gone', () => {
    const gone = stateContext({ runs: runRecords({ ...integrator, status: 'failed' }) })
    expect(needsYou(stoppedWorker, gone)?.id).toBe('needs-input')
  })

  it('counts a worker while a human holds it', () => {
    const held = stateContext({
      runs: runRecords(integrator),
      missionDetails: { mission_1: detail({ attempts: [attempt({ takeover_active: true, takeover_member_id: bob.id })] }) },
    })
    expect(needsYou(stoppedWorker, held)?.id).toBe('needs-input')
    expect(needsYou(stoppedWorker, { ...held, viewerID: bob.id, viewerRole: bob.role })?.id).toBe('control-hold')
  })
})

describe('stopped integrators', () => {
  const failed = record({ ...integrator, status: 'failed' })

  it('counts nothing until the swarm record loads', () => {
    expect(needsYou(failed, stateContext())).toBeUndefined()
  })

  it('counts nothing for a replaced integrator', () => {
    const replaced = stateContext({ missions: { mission_1: mission({ current_integrator_run_id: 'run_next' }) } })
    expect(needsYou(failed, replaced)).toBeUndefined()
  })
})

describe('background runs', () => {
  it('reach Needs you on a pending Aether approval', () => {
    const ctx = stateContext({ approvalsByRun: { run_1: [approval()] } })
    expect(needsYou(record({ mode: 'headless' }), ctx)?.id).toBe('permission')
  })

  it('leave an unreviewed finished run in its group, labelled by its outcome', () => {
    const failed = record({ mode: 'headless', status: 'failed', state: '', reason: 'exit status 1', outcome_unseen: true })
    expect(presentRun(failed, stateContext())).toEqual({ state: 'failed', reason: 'Failed: exit status 1' })
    expect(presentRun({ ...failed, status: 'completed' }, stateContext())).toEqual({ state: 'finished', reason: 'Finished' })
  })
})

describe('device cards', () => {
  const admin = { viewerID: 'mem_admin', viewerRole: 'admin' as const }
  const collaborator = { viewerID: bob.id, viewerRole: 'collaborator' as const }

  it.each([
    ['waiting on an invitation', pendingDevice(), 'laptop (github mallory) wants to join as collaborator', 'new member, collaborator'],
    ['of a member', pendingDevice({ member_id: bob.id, invitation_id: undefined, account: 'bob', display_name: 'Bob' }),
      'laptop (github bob) signed in as Bob', 'admits as: Bob (collaborator)'],
  ])('asks an admin to approve a device %s', (_, device, reason, admits) => {
    expect(deviceNeeds([device], admin, 'account')).toEqual([{ device, reason, admits, waitingSince: device.created_at }])
    expect(deviceNeeds([device], { ...collaborator, viewerID: 'mem_other' }, 'account')).toEqual([])
  })

  it('asks a member to approve only their own pending device', () => {
    const own = pendingDevice({ member_id: bob.id, invitation_id: undefined, account: 'bob', display_name: 'Bob' })
    const invited = pendingDevice({ id: 'dev_2' })
    const registered = pendingDevice({ id: 'dev_3', status: 'registered', member_id: bob.id, invitation_id: undefined })
    expect(deviceNeeds([own, invited, registered], collaborator, 'approved-devices')).toEqual([{
      device: own,
      reason: 'New device laptop signed in as you',
      admits: 'admits as: Bob (collaborator)',
      waitingSince: own.created_at,
    }])
  })

  it('asks a member to approve a device waiting on their own account link', () => {
    const linking = pendingDevice({ account: 'bob-gh', display_name: 'Bob' })
    expect(deviceNeeds([linking], collaborator, 'account')).toEqual([{
      device: linking,
      reason: 'laptop (github bob-gh) is waiting on your account link',
      admits: 'admits as: Bob (collaborator)',
      waitingSince: linking.created_at,
    }])
  })

  it('asks for a device registered under account once the server is under approved-devices', () => {
    const device = pendingDevice({ status: 'registered', member_id: bob.id, invitation_id: undefined, account: 'bob', display_name: 'Bob' })
    expect(deviceNeeds([device], admin, 'account')).toEqual([])
    expect(deviceNeeds([device], admin, 'approved-devices')).toEqual([{
      device,
      reason: 'laptop (github bob) signed in as Bob and is not approved',
      admits: 'admits as: Bob (collaborator)',
      waitingSince: device.created_at,
    }])
  })
})
