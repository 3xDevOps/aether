import type { Event } from '@/lib/types'
import { createRootStore } from '@/store'
import { applyEvent, hydrate } from '@/store/sync'
import { missionSeqOf } from '@/store/workflow-signals'
import { fakeApi, workspace } from '@/test/fixtures'

function event(seq: number, type: string, payload: unknown, run_id = ''): Event {
  return { id: `evt_${seq}`, seq, time: '2026-10-20T10:00:00Z', workspace_id: workspace.id, run_id, actor_id: '', type, payload }
}

describe('workflow signals', () => {
  it('counts git, pull request and mission events per run and mission', async () => {
    const store = createRootStore()
    const client = fakeApi()
    await hydrate(store, client)

    const events = [
      event(1, 'git.branch', { workspace_id: workspace.id, branch: 'aether/run_1', commit: 'abc1234', previous: '', forced: false }, 'run_1'),
      event(2, 'git.branch', { workspace_id: workspace.id, branch: 'aether/run_1', commit: 'def5678', previous: 'abc1234', forced: true }, 'run_1'),
      event(3, 'run.pr', { run_id: 'run_1', number: 42, url: 'https://github.com/acme/app/pull/42', state: 'merged', merged_at: '2026-10-20T10:00:00Z' }, 'run_1'),
      event(4, 'mission.changed', { mission_id: 'mission_1', change: 'task_finished', task_id: 'task_1' }),
      event(5, 'mission.changed', { mission_id: 'mission_1', deleted: true }),
    ]
    for (const ev of events) expect(await applyEvent(store, ev, client)).toBe(true)

    const s = store.getState()
    expect(s.gitSeq.run_1).toBe(2)
    expect(s.runs.run_1?.last_commit).toBe('def5678')
    expect(s.prSeq.run_1).toBe(1)
    expect(missionSeqOf('mission_1')(s)).toBe(2)
    expect(s.gitSeq.run_2).toBeUndefined()
  })
})
