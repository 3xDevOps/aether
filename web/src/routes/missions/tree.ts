import { swarmHandle } from '@/components/messages/handles'
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
  return detail.tasks.map((task) => {
    const needed = new Set((task.dependencies ?? []).map((dependency) => dependency.depends_on_task_id))
    return {
      task,
      title: taskTitle(task),
      dependsOn: detail.tasks.filter((other) => needed.has(other.id)),
      attempts: detail.attempts
        .filter((attempt) => attempt.task_id === task.id)
        .sort((a, b) => b.number - a.number)
        .map((attempt) => ({
          attempt,
          name: (attempt.run_id && swarmHandle(detail, attempt.run_id)?.name) || `Attempt ${attempt.number}`,
          earlierIntegrator: attempt.integrator_generation !== detail.mission.integrator_generation,
        })),
    }
  })
}

export function elapsed(from: string, to: string | number): string {
  const age = compactAge(from, typeof to === 'string' ? Date.parse(to) : to)
  return age === 'now' ? '<1m' : age
}
