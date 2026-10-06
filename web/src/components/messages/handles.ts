import { useEffect } from 'react'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import type { MissionDetail } from '@/store/missions'
import type { RunRecord } from '@/store/runs'

export interface Handle {
  name: string
  task?: string
}

/** Workers are numbered by task order, which mission.show returns oldest first. */
export function swarmHandle(detail: MissionDetail | undefined, runID: string): Handle | undefined {
  if (!detail) return undefined
  if (detail.mission.current_integrator_run_id === runID) return { name: 'Integrator' }
  const attempt = detail.attempts.find((item) => item.run_id === runID)
  if (!attempt) return undefined
  const index = detail.tasks.findIndex((task) => task.id === attempt.task_id)
  if (index < 0) return undefined
  const task = detail.tasks[index]
  const first = Math.min(...detail.attempts.filter((item) => item.task_id === task.id).map((item) => item.number))
  const name = `Worker ${index + 1}${attempt.number > first ? `, attempt ${attempt.number}` : ''}`
  return { name, task: task.revision?.title || task.pending_revision?.title }
}

const requested = new Map<string, Set<string>>()

async function loadMission(missionID: string): Promise<void> {
  try {
    const result = await api.missionShow(missionID)
    useStore.getState().setMissionDetail({
      mission: result.mission,
      tasks: result.tasks,
      attempts: result.attempts ?? [],
      submissions: result.submissions ?? [],
      diagnostics: result.diagnostics ?? [],
      questions: result.questions ?? [],
    })
  } catch {
    // Without the swarm the participant keeps its run label.
  }
}

export function useRunHandle(runID: string, run: RunRecord | undefined): Handle | undefined {
  const missionID = run?.mission_id
  const name = useStore((s) => (missionID ? swarmHandle(s.missionDetails[missionID], runID)?.name : undefined))
  const task = useStore((s) => (missionID ? swarmHandle(s.missionDetails[missionID], runID)?.task : undefined))
  useEffect(() => {
    if (!missionID || name) return
    const asked = requested.get(missionID) ?? new Set()
    if (asked.has(runID)) return
    requested.set(missionID, asked.add(runID))
    void loadMission(missionID)
  }, [missionID, runID, name])
  if (name) return { name, task }
  return run?.mission_role === 'integrator' ? { name: 'Integrator' } : undefined
}
