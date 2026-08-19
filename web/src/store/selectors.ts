// Sidebar shape: sessions with their runs nested, grouped and sorted so the
// things that need a human come first.

import {
  rollup,
  runState,
  stateLabel,
  stateRank,
  type PresentationState,
} from '@/lib/status'
import type { Member, Session } from '@/lib/types'
import type { RunRecord } from '@/store/runs'
import type { GroupBy } from '@/store/ui'

/**
 * The slice of the root state the sidebar derives from. Narrow on purpose:
 * components subscribe to exactly these fields and memoize, so a derived
 * array is never recomputed on unrelated store writes. The pending set is
 * here because a run holding a pending approval presents as needs-attention;
 * it arrives pre-derived (usePendingApprovalRuns) so an inbox refetch that
 * changed nothing keeps its identity.
 */
export interface SidebarInput {
  sessions: Record<string, Session>
  runs: Record<string, RunRecord>
  members: Record<string, Member>
  groupBy: GroupBy
  pending: Set<string>
}

export interface SidebarRun {
  run: RunRecord
  state: PresentationState
  owner?: Member
}

export interface SidebarSession {
  session: Session
  runs: SidebarRun[]
  state: PresentationState
  changedAt: string
}

export interface SidebarGroup {
  key: string
  label: string
  sessions: SidebarSession[]
}

function byAttention(a: { state: PresentationState; changedAt: string }, b: typeof a): number {
  const rank = stateRank(a.state) - stateRank(b.state)
  return rank !== 0 ? rank : b.changedAt.localeCompare(a.changedAt)
}

/** Worst state first, then most recently changed. */
export function sortRuns(runs: SidebarRun[]): SidebarRun[] {
  return [...runs].sort((a, b) =>
    byAttention(
      { state: a.state, changedAt: a.run.stateChangedAt },
      { state: b.state, changedAt: b.run.stateChangedAt },
    ),
  )
}

export function sidebarSessions(s: SidebarInput): SidebarSession[] {
  const runsBySession = new Map<string, SidebarRun[]>()
  for (const run of Object.values(s.runs)) {
    const entry = {
      run,
      state: runState(run.status, s.pending.has(run.id)),
      owner: s.members[run.member_id],
    }
    const list = runsBySession.get(run.session_id)
    if (list) list.push(entry)
    else runsBySession.set(run.session_id, [entry])
  }

  return Object.values(s.sessions).map((session) => {
    const runs = sortRuns(runsBySession.get(session.id) ?? [])
    return {
      session,
      runs,
      state: rollup(runs.map((r) => r.state)),
      changedAt: runs[0]?.run.stateChangedAt ?? session.created_at,
    }
  })
}

/**
 * Group by the session's rolled-up state, or by the member owning its most
 * recently changed run. Groups and sessions both sort worst-state-first, then
 * most-recently-changed-first.
 */
export function sidebarGroups(s: SidebarInput): SidebarGroup[] {
  const sessions = sidebarSessions(s).sort(byAttention)
  const groups = new Map<string, SidebarGroup>()

  for (const entry of sessions) {
    const [key, label] =
      s.groupBy === 'status' ? [entry.state, stateLabel[entry.state]] : ownerOf(entry)
    const group = groups.get(key)
    if (group) group.sessions.push(entry)
    else groups.set(key, { key, label, sessions: [entry] })
  }

  const order = [...groups.values()]
  if (s.groupBy === 'status') {
    return order.sort(
      (a, b) =>
        stateRank(a.key as PresentationState) - stateRank(b.key as PresentationState),
    )
  }
  return order.sort((a, b) => a.label.localeCompare(b.label))
}

function ownerOf(entry: SidebarSession): [string, string] {
  const owner = entry.runs[0]?.owner
  if (owner) return [owner.id, owner.display_name]
  const memberID = entry.runs[0]?.run.member_id
  return memberID ? [memberID, memberID] : ['unassigned', 'No runs']
}
