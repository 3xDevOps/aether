// Board shape: run cards dealt into Orca's three buckets. Pure over a narrow
// input so the component can memoize on exactly what it reads.

import { useMemo } from 'react'
import { runState, type PresentationState } from '@/lib/status'
import type { Member, Workspace } from '@/lib/types'
import { useStore } from '@/store'
import { isUnseen, type Ack } from '@/store/board'
import { usePendingApprovalRuns } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

export type Bucket = 'needs-you' | 'working' | 'done'

export interface BoardCard {
  run: RunRecord
  state: PresentationState
  owner?: Member
  workspace?: Workspace
  unseen: boolean
  paused: boolean
}

export interface BoardColumn {
  key: Bucket
  label: string
  cards: BoardCard[]
}

/**
 * An empty workspace shows every run: that is what the board falls back to
 * before hydration has named one.
 */
export interface BoardInput {
  workspace: string
  workspaces: Record<string, Workspace>
  runs: Record<string, RunRecord>
  members: Record<string, Member>
  acked: Record<string, Ack>
  pausedRuns: Record<string, boolean>
  /** Runs holding a pending approval, pre-derived so its identity is stable. */
  pending: Set<string>
}

export const bucketLabel: Record<Bucket, string> = {
  'needs-you': 'Needs You',
  working: 'Working',
  done: 'Done',
}

/**
 * Lifecycle to bucket. `needs-attention` covers both stalls and clean exits
 * waiting on run.close - the reason string on the card tells them apart.
 */
export function bucketOf(state: PresentationState): Bucket {
  switch (state) {
    case 'needs-attention':
      return 'needs-you'
    case 'working':
    case 'waiting':
      return 'working'
    default:
      return 'done'
  }
}

export interface BoardData {
  columns: BoardColumn[]
}

const at = (iso: string) => Date.parse(iso)

export function board(s: BoardInput): BoardData {
  const columns: Record<Bucket, BoardCard[]> = {
    'needs-you': [],
    working: [],
    done: [],
  }

  for (const run of Object.values(s.runs)) {
    if (s.workspace && run.workspace_id !== s.workspace) continue
    const state = runState(run.status, s.pending.has(run.id))
    columns[bucketOf(state)].push({
      run,
      state,
      owner: s.members[run.member_id],
      workspace: s.workspaces[run.workspace_id],
      unseen: isUnseen(s.acked, run),
      paused: state === 'working' && s.pausedRuns[run.id] === true,
    })
  }

  const newestFirst = (a: BoardCard, b: BoardCard) =>
    at(b.run.stateChangedAt) - at(a.run.stateChangedAt)

  return {
    columns: (Object.keys(columns) as Bucket[]).map((key) => ({
      key,
      label: bucketLabel[key],
      cards: columns[key].sort(newestFirst),
    })),
  }
}

export function useBoard(): BoardData {
  const workspace = useStore((s) => s.activeWorkspace)
  const workspaces = useStore((s) => s.workspaces)
  const runs = useStore((s) => s.runs)
  const members = useStore((s) => s.members)
  const acked = useStore((s) => s.acked)
  const pausedRuns = useStore((s) => s.pausedRuns)
  const pending = usePendingApprovalRuns()
  return useMemo(
    () =>
      board({ workspace, workspaces, runs, members, acked, pausedRuns, pending }),
    [workspace, workspaces, runs, members, acked, pausedRuns, pending],
  )
}
