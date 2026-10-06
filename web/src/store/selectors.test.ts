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
    expect(swarm.swarm).toMatchObject({ collapsed: true, counts: { working: 2, needsYou: 0, done: 1 } })
    expect(ids(swarm.swarm?.members ?? []).sort()).toEqual(['w1', 'w2', 'w3'])
  })

  it('lists the swarm in Needs you with only the members that need the viewer', () => {
    const blocked = worker('blocked', { status: 'needs-attention', reason: 'blocked: no database access' })
    const groups = runGroups(input([integrator, worker('w1'), blocked], {}, { missions: { mission_1: mission() } }))
    expect(ids(groups['needs-you'])).toEqual(['run_integrator'])
    const [swarm] = groups['needs-you']
    expect(swarm.state).toBe('working')
    expect(ids(swarm.children)).toEqual(['blocked'])
    expect(swarm.swarm?.counts).toEqual({ working: 1, needsYou: 1, done: 0 })
  })

  it("roots a replaced integrator's swarm at the current one", () => {
    const old = record({ id: 'old', mission_id: 'mission_1', mission_role: 'integrator', status: 'failed' })
    const groups = runGroups(input([old, integrator, worker('w1')], {}, { missions: { mission_1: mission() } }))
    expect(ids(groups.working)).toEqual(['run_integrator'])
    expect(groups.finished).toEqual([])
  })

  it('lists workers standalone when no integrator is listed', () => {
    const groups = runGroups(input([worker('w1'), worker('w2')]))
    expect(ids(groups.working).sort()).toEqual(['w1', 'w2'])
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
