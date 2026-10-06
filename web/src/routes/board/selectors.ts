import { dequal } from 'dequal'
import { useMemo, useRef } from 'react'
import type { RunActionCandidate } from '@/lib/commands'
import type { StateContext } from '@/lib/needs-you'
import { groupLabel, groupOf, presentRun, type RunGroup } from '@/lib/status'
import type { Workspace } from '@/lib/types'
import { useStore } from '@/store'
import { useStateContext } from '@/store/hooks'
import { isArchivable, type RunRecord } from '@/store/runs'
import { listedRuns, runGroups, runTrees, type RunRow, type RunTree, type RunsInput } from '@/store/selectors'

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
  hiddenByMine: boolean
}

const at = (iso: string) => Date.parse(iso)

function archivedRows(s: RunsInput): RunRow[] {
  const rows: RunRow[] = []
  for (const run of Object.values(s.ctx.runs)) {
    if (s.workspace && run.workspace_id !== s.workspace) continue
    if (s.mineOnly && run.member_id !== s.ctx.viewerID) continue
    if (!run.archived_at || !isArchivable(run.status)) continue
    const shown = presentRun(run, s.ctx)
    rows.push({
      run,
      state: shown.state,
      reason: shown.reason,
      group: groupOf(shown.state),
      waitingSince: run.stateChangedAt,
      owner: s.ctx.members[run.member_id],
    })
  }
  return rows
}

export function board(s: RunsInput): BoardData {
  const groups = runGroups(s)
  return {
    columns: (Object.keys(groups) as RunGroup[]).map((key) => ({
      key,
      label: groupLabel[key],
      cards: groups[key],
    })),
    archivedCards: runTrees(archivedRows(s), s.ctx).sort(
      (a, b) => at(b.run.archived_at ?? '') - at(a.run.archived_at ?? ''),
    ),
    hiddenByMine:
      s.mineOnly &&
      Object.values(s.ctx.runs).some(
        (run) => (!s.workspace || run.workspace_id === s.workspace) && run.member_id !== s.ctx.viewerID,
      ),
  }
}

// Unchanged cards keep their previous object so the memoized RunCard skips them.
function reuseCards(data: BoardData, previous: Map<string, BoardCard>): Map<string, BoardCard> {
  const next = new Map<string, BoardCard>()
  const keep = (card: BoardCard) => {
    const old = previous.get(card.run.id)
    const kept = old && dequal(old, card) ? old : card
    next.set(card.run.id, kept)
    return kept
  }
  for (const column of data.columns) column.cards = column.cards.map(keep)
  data.archivedCards = data.archivedCards.map(keep)
  return next
}

export function useBoard(): BoardData {
  const ctx = useStateContext()
  const workspace = useStore((s) => s.activeWorkspace)
  const mineOnly = useStore((s) => s.mineOnly)
  const cards = useRef(new Map<string, BoardCard>())
  return useMemo(() => {
    const data = board({ workspace, mineOnly, ctx })
    cards.current = reuseCards(data, cards.current)
    return data
  }, [workspace, mineOnly, ctx])
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
