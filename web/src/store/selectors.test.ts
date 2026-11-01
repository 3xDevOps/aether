import { toRecord, type RunRecord } from '@/store/runs'
import {
  descendantRuns,
  listedRuns,
  needsYouByWorkspace,
  runGroups,
  sidebarGroups,
  type RunsInput,
} from '@/store/selectors'
import {
  alice,
  bob,
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
const stopped = record({ id: 'stopped', status: 'needs-attention', state: 'needs-you', started_at: '2026-08-14T10:05:00Z' })
const elsewhere = record({
  id: 'elsewhere',
  workspace_id: otherWorkspace.id,
  status: 'needs-attention',
  state: 'needs-you',
  started_at: '2026-08-14T10:03:00Z',
})
const done = record({ id: 'done', status: 'merged', finished_at: '2026-08-14T10:30:00Z' })
const failed = record({ id: 'failed', status: 'failed', finished_at: '2026-08-14T10:10:00Z' })
const interrupted = record({ id: 'interrupted', status: 'interrupted', finished_at: '2026-08-14T10:05:00Z' })

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

  it('sorts Working by latest change and Finished with failures and interruptions first', () => {
    const groups = runGroups(input([working, newer, done, interrupted, failed]))
    expect(ids(groups.working)).toEqual(['newer', 'working'])
    expect(ids(groups.finished)).toEqual(['failed', 'interrupted', 'done'])
  })

  it('narrows Working and Finished to the viewer under Mine, never Needs you', () => {
    const theirs = record({ id: 'theirs', member_id: bob.id })
    const theirsStopped = record({ id: 'theirs-stopped', member_id: bob.id, status: 'needs-attention', state: 'needs-you' })
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

describe('child runs', () => {
  const parent = record({ id: 'parent' })
  const child = (id: string, parentID: string, over: Parameters<typeof run>[0] = {}) =>
    record({ id, parent_run_id: parentID, root_run_id: 'parent', child_purpose: `review ${id}`, harness: 'codex', ...over })

  it('folds children at any depth under their root, and a child that needs the viewer surfaces the root in Needs you', () => {
    const asking = child('grandchild', 'c1', { status: 'needs-attention', state: 'needs-you', started_at: '2026-08-14T09:30:00Z' })
    const groups = runGroups(input([parent, child('c1', 'parent'), child('c2', 'parent', { status: 'completed' }), asking]))
    expect(ids(groups['needs-you'])).toEqual(['parent'])
    expect(groups.working).toEqual([])
    const [tree] = groups['needs-you']
    expect(ids(tree.children)).toEqual(['grandchild'])
    expect(tree.childRuns).toMatchObject({ collapsed: true, counts: { working: 1, needsYou: 1, done: 1, failed: 0 } })
    expect(ids(tree.childRuns?.members ?? []).sort()).toEqual(['c1', 'c2', 'grandchild'])
    expect(tree.waitingSince).toBe(asking.stateChangedAt)
  })

  it('lists a child on its own when its parent is not loaded', () => {
    expect(ids(runGroups(input([child('c1', 'gone')])).working)).toEqual(['c1'])
  })

  it('lists descendants nearest first', () => {
    const runs = [parent, child('c2', 'c1', { created_at: '2026-08-14T09:00:00Z' }), child('c1', 'parent', { created_at: '2026-08-14T10:00:00Z' })]
    expect(descendantRuns(Object.fromEntries(runs.map((r) => [r.id, r])), ['parent']).map((r) => r.id)).toEqual(['c1', 'c2'])
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

  it('lists every child run beneath its parent, not only those that need the viewer', () => {
    const groups = sidebarGroups(input([
      record({ id: 'parent' }),
      record({ id: 'c1', parent_run_id: 'parent', created_at: '2026-08-14T10:00:00Z' }),
      record({ id: 'c2', parent_run_id: 'parent', status: 'completed', created_at: '2026-08-14T11:00:00Z' }),
    ]))
    expect(ids(groups[0].runs)).toEqual(['parent'])
    expect(ids(groups[0].runs[0].children)).toEqual(['c1', 'c2'])
  })

  it('keeps every visible child run beneath its parent without inflating Needs you', () => {
    const childRun = (id: string, over: Parameters<typeof run>[0] = {}) => record({ id, parent_run_id: 'run_parent', ...over })
    const groups = sidebarGroups(input([
      record({ id: 'run_parent' }),
      childRun('working'),
      childRun('blocked', { status: 'needs-attention', state: 'needs-you', reason: 'blocked: no database access' }),
      childRun('done', { status: 'merged' }),
      childRun('archived', { status: 'merged', archived_at: '2026-08-15T00:00:00Z' }),
    ]))

    expect(groups.map((group) => [group.key, group.count])).toEqual([['needs-you', 1]])
    expect(ids(groups[0].runs)).toEqual(['run_parent'])
    expect(ids(groups[0].runs[0].children)).toEqual(['blocked', 'done', 'working'])
  })

  it('applies Mine to child runs', () => {
    const root = record({ id: 'run_parent' })
    const mine = record({ id: 'mine', parent_run_id: root.id })
    const theirs = record({ id: 'theirs', member_id: bob.id, parent_run_id: root.id })
    const scope = input([root, mine, theirs])
    expect(ids(sidebarGroups(scope)[0].runs[0].children)).toEqual(['mine', 'theirs'])
    expect(ids(sidebarGroups({ ...scope, mineOnly: true })[0].runs[0].children)).toEqual(['mine'])
  })

  it('is empty without runs', () => {
    expect(sidebarGroups(input([]))).toEqual([])
  })
})

describe('needsYouByWorkspace', () => {
  it('counts runs needing the viewer per workspace', () => {
    const theirs = record({ id: 'theirs', member_id: bob.id, status: 'needs-attention', state: 'needs-you' })
    expect(needsYouByWorkspace(input([working, stopped, elsewhere, theirs]).ctx)).toEqual({
      [workspace.id]: 1,
      [otherWorkspace.id]: 1,
    })
  })
})

describe('the Needs you count', () => {
  const ready = record({ id: 'ready', status: 'needs-attention', state: 'finished', review: 'ready', review_changed_at: '2026-08-14T10:06:00Z' })
  const later = record({ id: 'later', status: 'needs-attention', state: 'finished', review: 'later' })
  const headless = record({ id: 'headless', mode: 'headless', status: 'completed', review: 'ready' })
  const accepted = record({ id: 'accepted', status: 'completed', review: 'accepted' })
  const agents = record({ id: 'agents', status: 'running', state: 'waiting-agents' })
  const waiting = record({ id: 'waiting', status: 'needs-attention', state: 'waiting' })
  const runs = [stopped, ready, later, headless, accepted, agents, waiting]

  it('counts needs-you and ready runs alike in the sidebar, the badge and the list', () => {
    const sidebar = sidebarGroups(input(runs)).find((group) => group.key === 'needs-you')
    expect(sidebar?.count).toBe(3)
    expect(ids(sidebar?.runs ?? [])).toEqual(['headless', 'stopped', 'ready'])
    expect(needsYouByWorkspace(input(runs).ctx)).toEqual({ [workspace.id]: 3 })
    expect(listedRuns(workspace.id, input(runs).ctx).filter((row) => row.needsYou).map((row) => row.run.id).sort())
      .toEqual(['headless', 'ready', 'stopped'])
  })

  it('lists a run answered Not yet quietly under Ready for review, outside the count', () => {
    const groups = sidebarGroups(input(runs))
    expect(groups.map((group) => group.key)).toEqual(['needs-you', 'working', 'review', 'finished'])
    expect(ids(groups.find((group) => group.key === 'review')?.runs ?? [])).toEqual(['later'])
    expect(ids(groups.find((group) => group.key === 'working')?.runs ?? []).sort()).toEqual(['agents', 'waiting'])
  })

  it('keeps a ready run in Needs you after its owner opens it', () => {
    const opened = { ...ready, outcome_unseen: false, finish_unopened: false }
    expect(needsYouByWorkspace(input([opened]).ctx)).toEqual({ [workspace.id]: 1 })
  })
})

describe('listedRuns', () => {
  it('lists one workspace in group order', () => {
    const rows = listedRuns(workspace.id, input([done, working, stopped, elsewhere, failed]).ctx)
    expect(ids(rows)).toEqual(['stopped', 'working', 'failed', 'done'])
    expect(rows[0].owner).toEqual(alice)
  })
})
