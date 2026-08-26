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
    status: 'merged',
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
})
