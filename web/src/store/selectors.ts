// Sidebar shape: the runs of the active workspace, grouped and sorted so the
// things that need a human come first.

import {
  runState,
  stateLabel,
  stateRank,
  type PresentationState,
} from '@/lib/status'
import type { Member } from '@/lib/types'
import type { RunRecord } from '@/store/runs'
import type { GroupBy } from '@/store/ui'

/**
 * The slice of the root state the sidebar derives from. Narrow on purpose:
 * components subscribe to exactly these fields and memoize, so a derived
 * array is never recomputed on unrelated store writes. The pending set is
 * here because a run holding a pending approval presents as needs-attention;
 * it arrives pre-derived (usePendingApprovalRuns) so an inbox refetch that
 * changed nothing keeps its identity.
 *
 * An empty workspace shows every run: that is what the board falls back to
 * before hydration has named one.
 */
export interface SidebarInput {
  workspace: string
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

export interface SidebarGroup {
  key: string
  label: string
  runs: SidebarRun[]
}

function byAttention(
  a: { state: PresentationState; changedAt: string },
  b: typeof a,
): number {
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

/** Every run in scope, worst state and most recent change first. */
export function sidebarRuns(s: SidebarInput): SidebarRun[] {
  const entries: SidebarRun[] = []
  for (const run of Object.values(s.runs)) {
    if (s.workspace && run.workspace_id !== s.workspace) continue
    entries.push({
      run,
      state: runState(run.status, s.pending.has(run.id)),
      owner: s.members[run.member_id],
    })
  }
  return sortRuns(entries)
}

/**
 * Group by run state, or by the owning member. Groups and runs both sort
 * worst-state-first, then most-recently-changed-first.
 */
export function sidebarGroups(s: SidebarInput): SidebarGroup[] {
  const groups = new Map<string, SidebarGroup>()

  for (const entry of sidebarRuns(s)) {
    const [key, label] =
      s.groupBy === 'status'
        ? [entry.state, stateLabel[entry.state]]
        : ownerOf(entry)
    const group = groups.get(key)
    if (group) group.runs.push(entry)
    else groups.set(key, { key, label, runs: [entry] })
  }

  const order = [...groups.values()]
  if (s.groupBy === 'status') {
    return order.sort(
      (a, b) =>
        stateRank(a.key as PresentationState) -
        stateRank(b.key as PresentationState),
    )
  }
  return order.sort((a, b) => a.label.localeCompare(b.label))
}

function ownerOf(entry: SidebarRun): [string, string] {
  if (entry.owner) return [entry.owner.id, entry.owner.display_name]
  return [entry.run.member_id, entry.run.member_id]
}
