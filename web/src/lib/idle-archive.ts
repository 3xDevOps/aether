import type { RunActionCandidate } from '@/lib/commands'
import { formatDuration } from '@/lib/format'
import { needsYou, type StateContext } from '@/lib/needs-you'
import { allowed } from '@/lib/permissions'
import type { Member } from '@/lib/types'
import type { Capability } from '@/store/hooks'
import { unfinishedChildren } from '@/store/children'
import type { RunRecord } from '@/store/runs'

/** How long a live session must sit idle and done before bulk archive offers to close it. */
export const idleArchiveAfterMs = 24 * 60 * 60 * 1000

export interface IdleCandidate {
  run: RunRecord
  /** Why it is offered, or why it is not. */
  reason: string
}

export interface IdleArchivePlan {
  /** Live, idle and done for over a day: offered to close and archive. */
  idle: IdleCandidate[]
  /** Other live sessions the member may act on, with what keeps each one. */
  excluded: IdleCandidate[]
}

/** Live sessions whose agent is done and has been quiet for over a day; the rest of the live ones say why they are not. */
export function idleArchivePlan(
  candidates: RunActionCandidate[],
  ctx: StateContext,
  cap: Capability,
  self: { id: string | null; role: Member['role'] | null },
): IdleArchivePlan {
  const plan: IdleArchivePlan = { idle: [], excluded: [] }
  if (!cap.hasMethod('run.archive') || !cap.hasMethod('run.close')) return plan
  for (const { run, workspace } of candidates) {
    if (run.archived_at || (run.status !== 'running' && run.status !== 'needs-attention')) continue
    if (!allowed('kill', self, { owner: run.member_id, protected: run.protected, steerOthers: workspace?.steer_others })) continue
    const held = holds(run, ctx)
    if (held) {
      plan.excluded.push({ run, reason: `Excluded: ${held}` })
      continue
    }
    const idleFor = ctx.now - Date.parse(run.stateChangedAt)
    if (!(idleFor > idleArchiveAfterMs)) {
      plan.excluded.push({ run, reason: `Excluded: active ${formatDuration(Math.max(0, idleFor))} ago` })
      continue
    }
    plan.idle.push({ run, reason: `Idle ${formatDuration(idleFor)}, ${run.state === 'answered' ? 'replied' : 'turn ended without a report'}` })
  }
  return plan
}

/** What keeps a live session from counting as done, in the words its row shows; undefined when it is done. */
function holds(run: RunRecord, ctx: StateContext): string | undefined {
  if (needsYou(run, ctx)) return 'needs you'
  if (ctx.pausedRuns[run.id]) return 'paused'
  if (run.pending_inputs?.length) return 'a request is pending'
  if (unfinishedChildren(ctx.runs, run).length > 0) return 'subsessions still running'
  if (run.cause === 'tasks-held') return 'background tasks still running'
  switch (run.state) {
    case 'answered':
      return undefined
    case 'waiting':
    case 'idle':
      return run.cause === 'turn-end' && !run.wait ? undefined : 'waiting on a reported condition'
    case 'waiting-ci':
      return 'waiting on CI'
    case 'waiting-agents':
      return 'waiting on agents'
    case 'waiting-tools':
      return 'waiting on tools'
    case 'finished':
      return 'its reported success awaits your review'
    case 'working':
      return 'working'
    default:
      return 'not done yet'
  }
}
