import { toRecord, type RunRecord } from '@/store/runs'
import {
  listedRuns,
  needsYouByWorkspace,
  runGroups,
  sidebarGroups,
  type RunsInput,
} from '@/store/selectors'
import {
  alice,
  approval,
  bob,
  mission,
  otherWorkspace,
  run,
  stateContext,
  workspace,
} from '@/test/fixtures'

function record(over: Parameters<typeof run>[0]): RunRecord {
  return toRecord(run(over))
}

function input(runs: RunRecord[], over: Partial<RunsInput> = {}, ctx = {}): RunsInput {
  return {
    workspace: workspace.id,
    mineOnly: false,
    ctx: stateContext({ runs: Object.fromEntries(runs.map((r) => [r.id, r])), ...ctx }),
    ...over,
  }
}

const ids = (trees: { run: { id: string } }[]) => trees.map((tree) => tree.run.id)

const working = record({ id: 'working', status: 'running', started_at: '2026-08-14T10:02:00Z' })
const newer = record({ id: 'newer', status: 'running', started_at: '2026-08-14T10:09:00Z' })
const stopped = record({ id: 'stopped', status: 'needs-attention', started_at: '2026-08-14T10:05:00Z' })
const elsewhere = record({
  id: 'elsewhere',
  workspace_id: otherWorkspace.id,
  status: 'needs-attention',
  started_at: '2026-08-14T10:03:00Z',
})
const done = record({ id: 'done', status: 'merged', finished_at: '2026-08-14T10:30:00Z' })
const failed = record({ id: 'failed', status: 'failed', finished_at: '2026-08-14T10:10:00Z' })

describe('runGroups', () => {
  it('lists Needs you from every workspace and the others from the active one', () => {
    const otherWorking = record({ id: 'other-working', workspace_id: otherWorkspace.id })
    const groups = runGroups(input([working, stopped, elsewhere, otherWorking, done]))
    expect(ids(groups['needs-you'])).toEqual(['elsewhere', 'stopped'])
    expect(ids(groups.working)).toEqual(['working'])
    expect(ids(groups.finished)).toEqual(['done'])
  })

  it('names the workspace of a row outside the active one', () => {
    const [outside, inside] = runGroups(input([stopped, elsewhere]))['needs-you']
    expect(outside.workspaceName).toBe(otherWorkspace.name)
    expect(inside.workspaceName).toBeUndefined()
    expect(runGroups(input([elsewhere], { workspace: '' }))['needs-you'][0].workspaceName).toBeUndefined()
  })

  it('sorts Needs you oldest wait first, from when the request was made', () => {
    const asked = record({ id: 'asked', started_at: '2026-08-14T10:00:00Z' })
    const groups = runGroups(
      input([stopped, asked], {}, { approvalsByRun: { asked: [approval({ run_id: 'asked', created_at: '2026-08-14T10:15:00Z' })] } }),
    )
    expect(ids(groups['needs-you'])).toEqual(['stopped', 'asked'])
    expect(groups['needs-you'][1].waitingSince).toBe('2026-08-14T10:15:00Z')
  })

  it('sorts Working by latest change and Finished with failures first', () => {
    const groups = runGroups(input([working, newer, done, failed]))
    expect(ids(groups.working)).toEqual(['newer', 'working'])
    expect(ids(groups.finished)).toEqual(['failed', 'done'])
  })

  it('narrows Working and Finished to the viewer under Mine, never Needs you', () => {
    const theirs = record({ id: 'theirs', member_id: bob.id })
    const theirsStopped = record({ id: 'theirs-stopped', member_id: bob.id, status: 'needs-attention' })
    const groups = runGroups(
      input([working, theirs, theirsStopped], { mineOnly: true }, { viewerID: bob.id, viewerRole: bob.role }),
    )
    expect(ids(groups['needs-you'])).toEqual(['theirs-stopped'])
    expect(ids(groups.working)).toEqual(['theirs'])
  })

  it('hides archived finished runs but never a live one', () => {
    const archived = { ...done, archived_at: '2026-08-15T00:00:00Z' }
    const live = { ...working, id: 'live', archived_at: '2026-08-15T00:00:00Z' }
    const groups = runGroups(input([archived, live]))
    expect(ids(groups.finished)).toEqual([])
    expect(ids(groups.working)).toEqual(['live'])
  })
})

describe('swarms', () => {
  const integrator = record({ id: 'run_integrator', mission_id: 'mission_1', mission_role: 'integrator' })
  const worker = (id: string, over: Parameters<typeof run>[0] = {}) =>
    record({
      id,
      member_id: bob.id,
      mission_id: 'mission_1',
      mission_role: 'worker',
      integrator_run_id: integrator.id,
      mode: 'headless',
      ...over,
    })

  it('folds a swarm into one collapsed entry with its counts', () => {
    const groups = runGroups(
      input([integrator, worker('w1'), worker('w2', { status: 'needs-attention' }), worker('w3', { status: 'completed' })]),
    )
    expect(ids(groups.working)).toEqual(['run_integrator'])
    const [swarm] = groups.working
    expect(swarm.children).toEqual([])
    expect(swarm.swarm).toMatchObject({ collapsed: true, counts: { working: 2, needsYou: 0, done: 1, failed: 0 } })
    expect(ids(swarm.swarm?.members ?? []).sort()).toEqual(['w1', 'w2', 'w3'])
  })

  it('lists the swarm in Needs you with only the members that need the viewer', () => {
    const blocked = worker('blocked', { status: 'needs-attention', reason: 'blocked: no database access' })
    const groups = runGroups(input([integrator, worker('w1'), blocked], {}, { missions: { mission_1: mission() } }))
    expect(ids(groups['needs-you'])).toEqual(['run_integrator'])
    const [swarm] = groups['needs-you']
    expect(swarm.state).toBe('working')
    expect(ids(swarm.children)).toEqual(['blocked'])
    expect(swarm.swarm?.counts).toEqual({ working: 1, needsYou: 1, done: 0, failed: 0 })
  })

  it("roots a replaced integrator's swarm at the current one", () => {
    const old = record({ id: 'old', mission_id: 'mission_1', mission_role: 'integrator', status: 'failed' })
    const groups = runGroups(input([old, integrator, worker('w1')], {}, { missions: { mission_1: mission() } }))
    expect(ids(groups.working)).toEqual(['run_integrator'])
    expect(groups.finished).toEqual([])
  })

  it('roots a swarm at its oldest worker when no integrator is listed', () => {
    const groups = runGroups(input([worker('w1'), worker('w2', { created_at: '2026-08-14T09:00:00Z' })]))
    expect(ids(groups.working)).toEqual(['w2'])
    expect(ids(groups.working[0].swarm?.members ?? [])).toEqual(['w1'])
  })
})

describe('sidebarGroups', () => {
  it('drops empty groups and counts what needs the viewer', () => {
    const groups = sidebarGroups(input([working, stopped, elsewhere]))
    expect(groups.map((group) => [group.label, group.count])).toEqual([
      ['Needs you', 2],
      ['Working', 1],
    ])
  })

  it('keeps every visible swarm run beneath its current integrator without inflating Needs you', () => {
    const swarmRun = (id: string, over: Parameters<typeof run>[0] = {}) => record({
      id, mission_id: 'mission_1', mission_role: 'worker', integrator_run_id: 'old', ...over,
    })
    const groups = sidebarGroups(input([
      swarmRun('old', { mission_role: 'integrator', status: 'failed' }),
      swarmRun('run_integrator', { mission_role: 'integrator' }),
      swarmRun('working'),
      swarmRun('blocked', { status: 'needs-attention', reason: 'blocked: no database access' }),
      swarmRun('done', { status: 'merged' }),
      swarmRun('archived', { status: 'merged', archived_at: '2026-08-15T00:00:00Z' }),
    ], {}, { missions: { mission_1: mission() } }))

    expect(groups.map((group) => [group.key, group.count])).toEqual([['needs-you', 1]])
    expect(ids(groups[0].runs)).toEqual(['run_integrator'])
    expect(ids(groups[0].runs[0].children)).toEqual(['blocked', 'done', 'old', 'working'])
  })

  it('applies Mine to swarm children without hiding Needs you context', () => {
    const root = record({ id: 'run_integrator', mission_id: 'mission_1', mission_role: 'integrator' })
    const mine = record({ id: 'mine', mission_id: 'mission_1', mission_role: 'worker' })
    const theirs = record({ id: 'theirs', member_id: bob.id, mission_id: 'mission_1', mission_role: 'worker' })
    const scope = input([root, mine, theirs], {}, { missions: { mission_1: mission() } })
    expect(ids(sidebarGroups(scope)[0].runs[0].children)).toEqual(['mine', 'theirs'])
    expect(ids(sidebarGroups({ ...scope, mineOnly: true })[0].runs[0].children)).toEqual(['mine'])

    scope.ctx.runs.theirs = { ...theirs, status: 'needs-attention', reason: 'blocked: no database access' }
    const [waiting] = sidebarGroups({ ...scope, mineOnly: true })
    expect(waiting.key).toBe('needs-you')
    expect(waiting.count).toBe(1)
    expect(ids(waiting.runs[0].children)).toEqual(['mine', 'theirs'])
  })

  it('keeps active workers visible under a missing-integrator fallback outside the selected workspace', () => {
    const worker = (id: string, over: Parameters<typeof run>[0] = {}) => record({
      id, mission_id: 'mission_1', mission_role: 'worker', workspace_id: otherWorkspace.id, ...over,
    })
    const groups = sidebarGroups(input([
      worker('root', { created_at: '2026-08-14T09:00:00Z' }),
      worker('working'),
      worker('blocked', { status: 'needs-attention', reason: 'blocked: no database access' }),
    ], { mineOnly: true }))

    expect(ids(groups[0].runs)).toEqual(['root'])
    expect(groups[0].count).toBe(1)
    expect(groups[0].runs[0].children.map((row) => [row.run.id, row.workspaceName])).toEqual([
      ['blocked', otherWorkspace.name], ['working', otherWorkspace.name],
    ])
  })

  it('is empty without runs', () => {
    expect(sidebarGroups(input([]))).toEqual([])
  })
})

describe('needsYouByWorkspace', () => {
  it('counts runs needing the viewer per workspace', () => {
    const theirs = record({ id: 'theirs', member_id: bob.id, status: 'needs-attention' })
    expect(needsYouByWorkspace(input([working, stopped, elsewhere, theirs]).ctx)).toEqual({
      [workspace.id]: 1,
      [otherWorkspace.id]: 1,
    })
  })
})

describe('listedRuns', () => {
  it('lists one workspace in group order', () => {
    const rows = listedRuns(workspace.id, input([done, working, stopped, elsewhere, failed]).ctx)
    expect(ids(rows)).toEqual(['stopped', 'working', 'failed', 'done'])
    expect(rows[0].owner).toEqual(alice)
  })
})
