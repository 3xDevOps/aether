import { closeState } from '@/lib/close-check'
import type { RunActionCandidate } from '@/lib/commands'
import { needsYou, type StateContext } from '@/lib/needs-you'
import { allowed } from '@/lib/permissions'
import type { Member, RunCloseCheck } from '@/lib/types'
import type { Capability } from '@/store/hooks'
import { unfinishedChildren } from '@/store/children'
import { isTerminal, type RunRecord } from '@/store/runs'

/** How long a live session must sit idle and done before bulk archive offers to close it. */
export const idleArchiveAfterMs = 24 * 60 * 60 * 1000

export interface IdleCandidate {
  run: RunRecord
  /** Why it is offered, or what keeps it. */
  reason: string
  /** The session as the plan saw it; a close is skipped when it changed since. */
  asked: string
}

export interface IdleArchivePlan {
  /** Live sessions whose agent is done; how long each has been idle comes from its close check. */
  done: IdleCandidate[]
  /** Other live sessions the member may act on, with what keeps each one. */
  excluded: IdleCandidate[]
}

export interface IdleRow extends IdleCandidate {
  check: RunCloseCheck
  /** Merged when a merged pull request holds its last commit; abandoned otherwise, or when an older server cannot tell. */
  outcome: 'merged' | 'abandoned'
  /** The older server did not say whether its pull request merged. */
  outcomeUnknown: boolean
  /** Starts checked: nothing would be affected and its outcome is known. */
  clean: boolean
}

/** Live sessions whose agent is done; the rest of the live ones say why they are not. */
export function idleArchivePlan(
  candidates: RunActionCandidate[],
  ctx: StateContext,
  cap: Capability,
  self: { id: string | null; role: Member['role'] | null },
): IdleArchivePlan {
  const plan: IdleArchivePlan = { done: [], excluded: [] }
  if (!cap.hasMethod('run.archive') || !cap.hasMethod('run.close')) return plan
  for (const { run, workspace } of candidates) {
    if (run.archived_at || (run.status !== 'running' && run.status !== 'needs-attention')) continue
    if (!allowed('kill', self, { owner: run.member_id, protected: run.protected, steerOthers: workspace?.steer_others })) continue
    const held = holds(run, ctx)
    const asked = closeState(run)
    if (held) plan.excluded.push({ run, reason: held, asked })
    else plan.done.push({ run, reason: run.state === 'answered' ? 'replied' : 'turn ended without a report', asked })
  }
  return plan
}

/** Splits the done sessions by how long their check says they have been idle. */
export function idleRows(done: IdleCandidate[], checks: Map<string, RunCloseCheck>, now: number): { idle: IdleRow[]; recent: IdleCandidate[] } {
  const out: { idle: IdleRow[]; recent: IdleCandidate[] } = { idle: [], recent: [] }
  for (const candidate of done) {
    const check = checks.get(candidate.run.id) ?? { run_id: candidate.run.id, warnings: [], error: { code: 0, message: 'the server did not answer for this session' } }
    const idleFor = Math.max(0, now - Date.parse(check.idle_since ?? candidate.run.stateChangedAt))
    if (!check.error && idleFor <= idleArchiveAfterMs) {
      out.recent.push({ ...candidate, reason: `active ${compactSpan(idleFor)} ago` })
      continue
    }
    const outcomeUnknown = !check.error && check.merged_pull_request === undefined
    const outcome = check.merged_pull_request ? 'merged' : 'abandoned'
    // A merged close asserts the work is merged, so unmerged work is no warning for it.
    const warnings = check.warnings.filter((w) => !(outcome === 'merged' && w.kind === 'unmerged_work'))
    out.idle.push({
      ...candidate,
      reason: `idle ${compactSpan(idleFor)}, ${candidate.reason}`,
      check: { ...check, warnings },
      outcome,
      outcomeUnknown,
      clean: !check.error && !outcomeUnknown && warnings.length === 0,
    })
  }
  return out
}

/** "3d", "2h" or "5m": the largest whole unit. */
function compactSpan(ms: number): string {
  const minutes = Math.floor(ms / 60_000)
  if (minutes >= 24 * 60) return `${Math.floor(minutes / (24 * 60))}d`
  if (minutes >= 60) return `${Math.floor(minutes / 60)}h`
  return `${minutes}m`
}

/** What keeps a live session from counting as done, in the words its row shows; undefined when it is done. */
function holds(run: RunRecord, ctx: StateContext): string | undefined {
  const parent = run.parent_run_id && !run.detached ? ctx.runs[run.parent_run_id] : undefined
  if (parent && !isTerminal(parent.status)) return 'its parent session is still open'
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
