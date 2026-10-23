import { dequal } from 'dequal'
import { useMemo, useRef } from 'react'
import type { RunActionCandidate } from '@/lib/commands'
import { resolveMember } from '@/lib/member-name'
import type { StateContext } from '@/lib/needs-you'
import { extraRepoCount, repoLabel } from '@/lib/repos'
import { groupLabel, groupOf, presentRun, type RunGroup } from '@/lib/status'
import type { Workspace } from '@/lib/types'
import { useStore } from '@/store'
import { useStateContext } from '@/store/hooks'
import { isArchivable, type RunRecord } from '@/store/runs'
import { listedRuns, runGroups, runRows, runTrees, type RunRow, type RunTree, type RunsInput } from '@/store/selectors'

export type BoardCard = RunTree

export interface BoardColumn {
  key: Exclude<RunGroup, 'review'>
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
      group: groupOf(shown),
      since: run.stateChangedAt,
      waitingSince: run.stateChangedAt,
      owner: resolveMember(s.ctx, run.member_id).member,
    })
  }
  return rows
}

export function board(s: RunsInput): BoardData {
  const groups = runGroups(s)
  // Ready for review heads Finished: the work is done, and only the owner's answer is left.
  const cards = { ...groups, finished: [...groups.review, ...groups.finished] }
  return {
    columns: (['needs-you', 'working', 'finished'] as const).map((key) => ({
      key,
      label: groupLabel[key],
      cards: cards[key],
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

export function mapRuns(s: RunsInput): { cards: BoardCard[]; archivedCards: BoardCard[] } {
  const individual = (row: RunRow): BoardCard => ({
    ...row,
    children: [],
    workspaceName: !s.workspace || row.run.workspace_id !== s.workspace || extraRepoCount(row.run) > 0
      ? repoLabel(s.ctx.workspaces[row.run.workspace_id]?.name ?? row.run.workspace_id, row.run)
      : undefined,
  })
  return {
    cards: runRows(s.ctx)
      .filter((row) => row.group === 'needs-you' || (
        (!s.workspace || row.run.workspace_id === s.workspace) &&
        (!s.mineOnly || row.run.member_id === s.ctx.viewerID)
      ))
      .map(individual),
    archivedCards: archivedRows(s).map(individual),
  }
}

export function useMapRuns(workspace: string) {
  const ctx = useStateContext()
  const mineOnly = useStore((s) => s.mineOnly)
  return useMemo(() => mapRuns({ workspace, mineOnly, ctx }), [workspace, mineOnly, ctx])
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
