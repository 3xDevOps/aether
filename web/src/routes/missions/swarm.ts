import { useMemo } from 'react'
import { modeLabel } from '@/components/launch/modes'
import type { Tone } from '@/components/ui/status-dot'
import { useClock } from '@/lib/clock'
import type { StateContext } from '@/lib/needs-you'
import type { AgentInfo, Mission, MissionPhase } from '@/lib/types'
import { useStore } from '@/store'
import { runRows, stateContextOf } from '@/store/selectors'

export const phaseWord: Record<MissionPhase, string> = {
  planning: 'Planning',
  active: 'Active',
  completed: 'Completed',
  cancelled: 'Cancelled',
}

export const phaseSentence: Record<MissionPhase, string> = {
  planning: 'The integrator asks you clarifying questions only if it needs answers, then proposes tasks and starts the swarm.',
  active: 'Workers run. The integrator accepts their work, verifies and delivers the result, then reports success.',
  completed: 'The integrator reported success. Leftover workers were stopped.',
  cancelled: 'The swarm was cancelled. Its workers and integrator run are stopped.',
}

export const phaseTone: Record<MissionPhase, Tone> = {
  planning: 'working',
  active: 'working',
  completed: 'done',
  cancelled: 'neutral',
}

export function missionFinal(mission: Pick<Mission, 'phase'>): boolean {
  return mission.phase === 'completed' || mission.phase === 'cancelled'
}

const titleLimit = 80

export function objectiveTitle(objective: string): string {
  const lines = objective.split('\n').map((line) => line.trim()).filter(Boolean)
  const first = lines[0] ?? 'Swarm'
  return first.length <= titleLimit ? first : `${first.slice(0, titleLimit).trimEnd()}…`
}

export function integratorLabel(mission: Mission, agents: AgentInfo[] | null): string {
  const agent = agents?.find((item) => item.name === mission.integrator.harness)
  return `${agent?.display_name || mission.integrator.harness} · ${modeLabel(mission.integrator.mode)}`
}

export interface SwarmLine {
  counts: string
  needsYou: boolean
  reason?: string
  unread: number
}

function countsLabel(counts: { working: number; needsYou: number; done: number; failed: number }): string {
  return [
    counts.working > 0 && `${counts.working} working`,
    counts.needsYou > 0 && `${counts.needsYou} needs you`,
    counts.done > 0 && `${counts.done} done`,
    counts.failed > 0 && `${counts.failed} failed`,
  ].filter(Boolean).join(' · ') || 'No workers yet'
}

export function swarmLines(ctx: StateContext): Record<string, SwarmLine> {
  const counts: Record<string, { working: number; needsYou: number; done: number; failed: number }> = {}
  const lines: Record<string, SwarmLine> = {}
  for (const row of runRows(ctx)) {
    const missionID = row.run.mission_id
    if (!missionID || !row.run.mission_role) continue
    const line = (lines[missionID] ??= { counts: '', needsYou: false, unread: 0 })
    const count = (counts[missionID] ??= { working: 0, needsYou: 0, done: 0, failed: 0 })
    if (row.state === 'needs-you' && !line.needsYou) {
      line.needsYou = true
      line.reason = row.reason
    }
    if (row.run.mission_role === 'integrator') {
      if (ctx.missions[missionID]?.current_integrator_run_id === row.run.id && row.unread) {
        line.unread = row.unread
        line.reason ??= row.reason
      }
      continue
    }
    if (row.state === 'needs-you') count.needsYou++
    else if (row.group === 'working') count.working++
    else if (row.state === 'failed') count.failed++
    else count.done++
  }
  for (const [id, line] of Object.entries(lines)) line.counts = countsLabel(counts[id])
  return lines
}

export function useSwarmLines(): Record<string, SwarmLine> {
  const now = useClock()
  const key = useStore((s) => JSON.stringify(swarmLines(stateContextOf(s, now))))
  return useMemo(() => JSON.parse(key) as Record<string, SwarmLine>, [key])
}
