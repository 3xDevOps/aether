// Board shape: the sidebar's three run groups as columns. Pure over a narrow
// input so the component can memoize on exactly what it reads.

import { useMemo } from 'react'
import type { RunActionCandidate } from '@/lib/commands'
import { groupLabel, groupOf, presentRun, type RunGroup } from '@/lib/status'
import type { Workspace } from '@/lib/types'
import { useStore } from '@/store'
import { useStateContext } from '@/store/hooks'
import { isArchivable } from '@/store/runs'
import {
  runGroups,
  stateContextOf,
  type RunRow,
  type RunTree,
  type RunsInput,
} from '@/store/selectors'

/** One card: a run, or a swarm under its integrator with its members' counts. */
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
    // A live run can never be hidden: archiving is a server-side no-op
    // outside a final status, but this guard holds even so.
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

/** Each card's run followed by its swarm's members. */
export function cardRuns(cards: BoardCard[]): RunRow[] {
  return cards.flatMap((card) => [card, ...(card.swarm?.members ?? [])])
}

function candidates(rows: RunRow[], workspaces: Record<string, Workspace>): RunActionCandidate[] {
  return rows.map(({ run }) => ({ run, workspace: workspaces[run.workspace_id] }))
}

/** The Finished column's runs, swarm members included, which Archive closed runs sweeps. */
export function finishedRuns(data: BoardData, workspaces: Record<string, Workspace>): RunActionCandidate[] {
  return candidates(cardRuns(data.columns.find((c) => c.key === 'finished')?.cards ?? []), workspaces)
}

/** Every run in scope, archived ones included, which Release finished resources sweeps. */
export function allRuns(data: BoardData, workspaces: Record<string, Workspace>): RunActionCandidate[] {
  return candidates(
    [...data.columns.flatMap((column) => cardRuns(column.cards)), ...data.archivedCards],
    workspaces,
  )
}

/** The board as the store holds it now, for a snapshot taken outside render. */
export function currentBoard(): BoardData {
  const s = useStore.getState()
  return board({ workspace: s.activeWorkspace, mineOnly: s.mineOnly, ctx: stateContextOf(s, Date.now()) })
}
