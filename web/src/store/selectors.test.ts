import type { RunRecord } from '@/store/runs'
import { toRecord } from '@/store/runs'
import { sidebarGroups, type SidebarInput } from '@/store/selectors'
import { alice, bob, otherSession, run, session } from '@/test/fixtures'

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
    session_id: otherSession.id,
    member_id: bob.id,
    status: 'merged',
    finished_at: '2026-08-14T10:30:00Z',
  }),
}

const input: SidebarInput = {
  sessions: { [session.id]: session, [otherSession.id]: otherSession },
  runs,
  members: { [alice.id]: alice, [bob.id]: bob },
  groupBy: 'status',
  pending: new Set<string>(),
}

describe('sidebarGroups', () => {
  it('rolls a session up to its worst run and puts attention first', () => {
    const groups = sidebarGroups(input)

    expect(groups.map((g) => g.label)).toEqual(['Needs you', 'Done'])
    const first = groups[0].sessions[0]
    expect(first.session.id).toBe(session.id)
    expect(first.state).toBe('needs-attention')
    expect(first.runs.map((r) => r.run.id)).toEqual(['attention', 'working'])
  })

  it('attaches the owning member to each run', () => {
    const groups = sidebarGroups(input)
    expect(groups[0].sessions[0].runs[0].owner).toEqual(alice)
  })

  it('groups by member when asked', () => {
    const groups = sidebarGroups({ ...input, groupBy: 'member' })

    expect(groups.map((g) => g.label)).toEqual(['Alice', 'Bob'])
    expect(groups[0].sessions.map((s) => s.session.id)).toEqual([session.id])
  })

  it('keeps a session with no runs, marked idle', () => {
    const groups = sidebarGroups({ ...input, runs: {} })

    expect(groups).toHaveLength(1)
    expect(groups[0].label).toBe('Idle')
    expect(groups[0].sessions).toHaveLength(2)
  })
})
