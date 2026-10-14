import { workerName } from '@/components/messages/handles'
import { compactAge } from '@/lib/format'
import type { MissionAttempt, MissionTask } from '@/lib/types'
import type { MissionDetail } from '@/store/missions'

export interface AttemptNode {
  attempt: MissionAttempt
  name: string
  earlierIntegrator: boolean
}

export interface TaskNode {
  task: MissionTask
  title: string
  dependsOn: MissionTask[]
  attempts: AttemptNode[]
}

export function taskTitle(task: MissionTask): string {
  return task.revision?.title || task.pending_revision?.title || 'Untitled task'
}

export function missionTree(detail: MissionDetail): TaskNode[] {
  const byTask = new Map<string, MissionAttempt[]>()
  for (const attempt of detail.attempts) {
    const attempts = byTask.get(attempt.task_id)
    if (attempts) attempts.push(attempt)
    else byTask.set(attempt.task_id, [attempt])
  }
  return detail.tasks.map((task, index) => {
    const needed = new Set((task.dependencies ?? []).map((dependency) => dependency.depends_on_task_id))
    const attempts = [...(byTask.get(task.id) ?? [])].sort((a, b) => b.number - a.number)
    const first = attempts.at(-1)?.number ?? 0
    return {
      task,
      title: taskTitle(task),
      dependsOn: detail.tasks.filter((other) => needed.has(other.id)),
      attempts: attempts.map((attempt) => ({
        attempt,
        name: workerName(index, attempt.number, first),
        earlierIntegrator: attempt.integrator_generation !== detail.mission.integrator_generation,
      })),
    }
  })
}

export function elapsed(from: string, to: string | number): string {
  const age = compactAge(from, typeof to === 'string' ? Date.parse(to) : to)
  return age === 'now' ? '<1m' : age
}
