import { elapsed, missionTree } from '@/routes/missions/tree'
import type { MissionAttempt } from '@/lib/types'
import type { MissionDetail } from '@/store/missions'
import { mission, missionTask, missionTaskRevision } from '@/test/fixtures'

function attempt(over: Partial<MissionAttempt>): MissionAttempt {
  return {
    id: 'attempt_1', mission_id: 'mission_1', task_id: 'task_1', task_revision: 1, number: 1, dispatch_key: 'k',
    harness: 'claude', mode: 'headless', state: 'running', run_id: 'run_w1', authority_generation: 1,
    integrator_generation: 1, created_at: '2026-08-14T10:03:00Z', reserved_at: '2026-08-14T10:03:00Z', ...over,
  }
}

function detail(over: Partial<MissionDetail>): MissionDetail {
  return { mission: mission(), tasks: [], attempts: [], submissions: [], diagnostics: [], questions: [], ...over }
}

describe('missionTree', () => {
  it('orders attempts newest first and names workers', () => {
    const [node] = missionTree(detail({
      tasks: [missionTask()],
      attempts: [attempt({}), attempt({ id: 'attempt_2', number: 2, run_id: 'run_w2' })],
    }))
    expect(node.attempts.map((item) => item.name)).toEqual(['Worker 1, attempt 2', 'Worker 1'])
  })

  it('resolves dependencies once, in task order, ignoring unknown tasks', () => {
    const tasks = [
      missionTask(),
      missionTask({ id: 'task_2', revision: missionTaskRevision({ task_id: 'task_2', title: 'second' }) }),
      missionTask({
        id: 'task_3',
        revision: null,
        dependencies: ['task_2', 'task_1', 'task_2', 'task_9'].map((id) => ({ task_id: 'task_3', revision: 1, depends_on_task_id: id, depends_on_revision: 1 })),
      }),
    ]
    const nodes = missionTree(detail({ tasks }))
    expect(nodes[2].title).toBe('Untitled task')
    expect(nodes[2].dependsOn.map((task) => task.id)).toEqual(['task_1', 'task_2'])
    expect(nodes[0].dependsOn).toEqual([])
  })

  it('flags attempts from a replaced integrator', () => {
    const [node] = missionTree(detail({
      mission: mission({ integrator_generation: 2 }),
      tasks: [missionTask()],
      attempts: [attempt({}), attempt({ id: 'attempt_2', number: 2, integrator_generation: 2 })],
    }))
    expect(node.attempts.map((item) => item.earlierIntegrator)).toEqual([false, true])
  })
})

describe('elapsed', () => {
  it('reads under a minute as <1m and otherwise compacts', () => {
    expect(elapsed('2026-08-14T10:00:00Z', '2026-08-14T10:00:30Z')).toBe('<1m')
    expect(elapsed('2026-08-14T10:00:00Z', Date.parse('2026-08-14T10:05:00Z'))).toBe('5m')
  })
})
