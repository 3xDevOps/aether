import type { RunRecord } from '@/store/runs'
import { toRecord } from '@/store/runs'
import { sidebarGroups, sidebarRuns, type SidebarInput } from '@/store/selectors'
import { alice, bob, otherWorkspace, run, workspace } from '@/test/fixtures'

function record(over: Parameters<typeof run>[0]): RunRecord {
  return toRecord(run(over))
}

const runs: Record<string, RunRecord> = {
  working: record({ id: 'working', status: 'running' }),
  attention: record({
    id: 'attention',
    status: 'needs-attention',
    created_at: '2026-08-14T10:03:00Z',
    started_at: '2026-08-14T10:03:00Z',
  }),
  done: record({
    id: 'done',
    workspace_id: otherWorkspace.id,
    member_id: bob.id,
    status: 'completed',
    finished_at: '2026-08-14T10:30:00Z',
  }),
}

const input: SidebarInput = {
  workspace: '',
  runs,
  members: { [alice.id]: alice, [bob.id]: bob },
  groupBy: 'status',
  pending: new Set<string>(),
}

describe('sidebarRuns', () => {
  it('puts the worst state first, then the most recent change', () => {
    expect(sidebarRuns(input).map((r) => r.run.id)).toEqual([
      'attention',
      'working',
      'done',
    ])
  })

  it('attaches the owning member to each run', () => {
    expect(sidebarRuns(input)[0].owner).toEqual(alice)
  })

  it('narrows to the active workspace', () => {
    const scoped = sidebarRuns({ ...input, workspace: otherWorkspace.id })
    expect(scoped.map((r) => r.run.id)).toEqual(['done'])
  })

  it('surfaces a running run holding a pending approval as needs-attention', () => {
    const scoped = sidebarRuns({ ...input, pending: new Set(['working']) })
    expect(scoped.find((r) => r.run.id === 'working')?.state).toBe('needs-attention')
    // Two attention runs now, and the more recent change leads.
    expect(scoped.map((r) => r.run.id)).toEqual(['attention', 'working', 'done'])
  })
  it('surfaces a running run with unanswered questions as needs-attention', () => {
    const scoped = sidebarRuns({
      ...input,
      runs: {
        ...input.runs,
        working: record({ id: 'working', status: 'running', unanswered_questions: 1 }),
      },
    })
    expect(scoped.find((r) => r.run.id === 'working')?.state).toBe('needs-attention')
  })

  it('keeps a terminal run with unanswered questions in Needs you', () => {
    const finished = record({
      id: 'finished-question',
      status: 'failed',
      unanswered_questions: 1,
    })
    const scoped = sidebarRuns({
      ...input,
      runs: { ...input.runs, [finished.id]: finished },
    })
    expect(scoped.find((entry) => entry.run.id === finished.id)?.state).toBe('needs-attention')
  })
})

describe('sidebarGroups', () => {
  it('groups by state, worst group first', () => {
    const groups = sidebarGroups(input)

    expect(groups.map((g) => g.label)).toEqual(['Needs you', 'Working', 'Done'])
    expect(groups[0].runs.map((r) => r.run.id)).toEqual(['attention'])
    expect(groups[2].runs.map((r) => r.run.id)).toEqual(['done'])
  })

  it('groups by member when asked', () => {
    const groups = sidebarGroups({ ...input, groupBy: 'member' })

    expect(groups.map((g) => g.label)).toEqual(['Alice', 'Bob'])
    // Within a member, the worst-first sort still holds.
    expect(groups[0].runs.map((r) => r.run.id)).toEqual(['attention', 'working'])
    expect(groups[1].runs.map((r) => r.run.id)).toEqual(['done'])
  })

  it('yields no groups when the active workspace holds no runs', () => {
    expect(sidebarGroups({ ...input, workspace: workspace.id, runs: {} })).toEqual([])
  })

  const integrator = record({
    id: 'integrator',
    status: 'running',
    mission_id: 'mission_1',
    mission_role: 'integrator',
    integrator_run_id: 'integrator',
  })
  const worker = record({
    id: 'worker',
    member_id: bob.id,
    status: 'completed',
    mission_id: 'mission_1',
    mission_role: 'worker',
    integrator_run_id: integrator.id,
  })

  it('keeps subsessions under their integrator across owners and statuses', () => {
    const groups = sidebarGroups({
      ...input,
      groupBy: 'member',
      runs: { integrator, worker, attention: runs.attention },
    })
    expect(groups.map((group) => group.label)).toEqual(['Alice'])
    expect(groups[0].runs.map((entry) => entry.run.id)).toEqual(['attention', 'integrator'])
    expect(groups[0].runs[1].children.map((entry) => entry.run.id)).toEqual(['worker'])
    expect(groups[0].runs[1].children[0].owner).toEqual(bob)
  })

  it('raises a swarm to its most urgent child without changing individual states', () => {
    const groups = sidebarGroups({
      ...input,
      runs: { integrator, worker },
      pending: new Set([worker.id]),
    })
    expect(groups.map((group) => group.key)).toEqual(['needs-attention'])
    expect(groups[0].runs[0].state).toBe('working')
    expect(groups[0].runs[0].children[0].state).toBe('needs-attention')
  })

  it('orders subsessions by attention and retains standalone runs exactly once', () => {
    const failed = { ...worker, id: 'failed-worker', status: 'failed' as const }
    const groups = sidebarGroups({
      ...input,
      runs: { worker, integrator, failed, attention: runs.attention },
    })
    expect(groups.map((group) => group.key)).toEqual(['needs-attention', 'failed'])
    expect(groups.flatMap((group) => group.runs.map((entry) => entry.run.id)))
      .toEqual(['attention', 'integrator'])
    expect(groups[1].runs[0].children.map((entry) => entry.run.id)).toEqual(['failed-worker', 'worker'])
  })

  it.each(['missing', 'archived', 'other-workspace', 'other-mission'] as const)(
    'keeps a subsession visible when its integrator is %s',
    (kind) => {
      const parent = {
        ...integrator,
        ...(kind === 'archived' ? { status: 'merged' as const, archived_at: '2026-08-15T00:00:00Z' } : {}),
        ...(kind === 'other-workspace' ? { workspace_id: otherWorkspace.id } : {}),
        ...(kind === 'other-mission' ? { mission_id: 'mission_2' } : {}),
      }
      const groups = sidebarGroups({
        ...input,
        workspace: workspace.id,
        runs: kind === 'missing' ? { worker } : { worker, integrator: parent },
      })
      const roots = groups.flatMap((group) => group.runs)
      expect(roots.find((entry) => entry.run.id === worker.id)?.children).toEqual([])
      expect(roots.flatMap((entry) => entry.children)).toEqual([])
    },
  )
})
