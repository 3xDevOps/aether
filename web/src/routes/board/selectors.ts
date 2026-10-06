import { useMemo } from 'react'
import type { RunActionCandidate } from '@/lib/commands'
import type { StateContext } from '@/lib/needs-you'
import { groupLabel, groupOf, presentRun, type RunGroup } from '@/lib/status'
import type { Workspace } from '@/lib/types'
import { useStore } from '@/store'
import { useStateContext } from '@/store/hooks'
import { isArchivable, type RunRecord } from '@/store/runs'
import { listedRuns, runGroups, type RunTree, type RunsInput } from '@/store/selectors'

export type BoardCard = RunTree

export interface BoardColumn {
  key: RunGroup
  label: string
  cards: BoardCard[]
}

export interface BoardData {
  columns: BoardColumn[]
  /** Finished, archived runs in scope - hidden from Finished, shown behind its toggle. */
  archivedCards: BoardCard[]
}

const at = (iso: string) => Date.parse(iso)

export function board(s: RunsInput): BoardData {
  const groups = runGroups(s)
  const archivedCards: BoardCard[] = []
  for (const run of Object.values(s.ctx.runs)) {
    if (s.workspace && run.workspace_id !== s.workspace) continue
    // Defensive: the server already ignores archiving outside a final status.
    if (!run.archived_at || !isArchivable(run.status)) continue
    const shown = presentRun(run, s.ctx)
    archivedCards.push({
      run,
      state: shown.state,
      reason: shown.reason,
      group: groupOf(shown.state),
      waitingSince: run.stateChangedAt,
      owner: s.ctx.members[run.member_id],
      children: [],
    })
  }
  return {
    columns: (Object.keys(groups) as RunGroup[]).map((key) => ({
      key,
      label: groupLabel[key],
      cards: groups[key],
    })),
    archivedCards: archivedCards.sort(
      (a, b) => at(b.run.archived_at ?? '') - at(a.run.archived_at ?? ''),
    ),
  }
}

export function useBoard(): BoardData {
  const ctx = useStateContext()
  const workspace = useStore((s) => s.activeWorkspace)
  const mineOnly = useStore((s) => s.mineOnly)
  return useMemo(() => board({ workspace, mineOnly, ctx }), [workspace, mineOnly, ctx])
}

function candidates(runs: RunRecord[], workspaces: Record<string, Workspace>): RunActionCandidate[] {
  return runs.map((run) => ({ run, workspace: workspaces[run.workspace_id] }))
}

// The bulk actions sweep the whole workspace whoever owns the run: Needs you
// spans workspaces and Mine hides teammates' runs, so the columns cannot be used.
export function finishedRuns(workspace: string, ctx: StateContext): RunActionCandidate[] {
  const rows = listedRuns(workspace, ctx).filter((row) => row.group === 'finished')
  return candidates(rows.map((row) => row.run), ctx.workspaces)
}

export function workspaceRuns(workspace: string, ctx: StateContext): RunActionCandidate[] {
  const runs = Object.values(ctx.runs).filter((run) => !workspace || run.workspace_id === workspace)
  return candidates(runs, ctx.workspaces)
}
