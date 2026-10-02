import type { Run, RunInputRequest, RunStatus } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

/** A run plus its last execution-status change time, for sorting and acknowledgments. */
export type RunRecord = Run & {
  reason?: string
  stateChangedAt: string
}

export function toRecord(run: Run, previous?: RunRecord): RunRecord {
  const carry = previous && previous.status === run.status
  return {
    ...run,
    reason: run.reason ?? (carry ? previous.reason : undefined),
    stateChangedAt: carry
      ? previous.stateChangedAt
      : (run.finished_at ?? run.started_at ?? run.created_at),
  }
}

/** `records` without the runs `keep` rejects; the same object when none is. */
function pruneRuns<T>(
  records: Record<string, T>,
  keep: (runID: string) => boolean,
): Record<string, T> {
  let pruned = records
  for (const runID in records) {
    if (keep(runID)) continue
    if (pruned === records) pruned = { ...records }
    delete pruned[runID]
  }
  return pruned
}

export interface RunsSlice {
  runs: Record<string, RunRecord>
  setRuns: (runs: Run[]) => void
  upsertRun: (run: Run) => void
  removeRun: (runID: string) => void
  applyRunInput: (runID: string, requests: RunInputRequest[]) => void
  applyRunStatus: (
    runID: string,
    to: RunStatus,
    reason: string | undefined,
    time: string,
  ) => void
  applyLastCommit: (runID: string, commit: string, time: string) => void
  applyRunTitle: (runID: string, title: string) => void
  applyRunProtected: (runID: string, isProtected: boolean) => void
  applyRunArchived: (
    runID: string,
    archivedAt: string | null,
    deletesAt: string | null,
  ) => void
}

export const createRunsSlice: SliceCreator<RunsSlice> = (set) => ({
  runs: {},
  setRuns: (runs) =>
    set((s) => {
      const records = Object.fromEntries(
        runs.map((r) => [r.id, toRecord(r, s.runs[r.id])]),
      )
      const listed = (runID: string) => runID in records
      return {
        runs: records,
        terminalWriteIntents: pruneRuns(s.terminalWriteIntents, listed),
        terminalControlSessions: pruneRuns(s.terminalControlSessions, listed),
      }
    }),
  upsertRun: (run) =>
    set((s) => {
      const current = s.runs[run.id]
      // Only hydration and run.input replace this independent stream state.
      // A late route/launch snapshot must not resurrect a resolved request.
      const next = toRecord(run, current)
      next.pending_inputs = current?.pending_inputs ?? run.pending_inputs
      return { runs: { ...s.runs, [run.id]: next } }
    }),
  removeRun: (runID) =>
    set((s) => {
      const other = (id: string) => id !== runID
      return {
        runs: pruneRuns(s.runs, other),
        terminalWriteIntents: pruneRuns(s.terminalWriteIntents, other),
        terminalControlSessions: pruneRuns(s.terminalControlSessions, other),
      }
    }),
  applyRunStatus: (runID, to, reason, time) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current) return {}
      const next: RunRecord = { ...current, status: to, reason, stateChangedAt: time }
      if (to === 'running' && !next.started_at) next.started_at = time
      if (isTerminal(to)) next.finished_at = time
      return { runs: { ...s.runs, [runID]: next } }
    }),

  applyRunInput: (runID, requests) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current) return {}
      return { runs: { ...s.runs, [runID]: { ...current, pending_inputs: requests } } }
    }),

  applyLastCommit: (runID, commit, time) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current) return {}
      return {
        runs: {
          ...s.runs,
          [runID]: { ...current, last_commit: commit, last_commit_at: time },
        },
      }
    }),

  applyRunTitle: (runID, title) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current || current.title === title) return {}
      return { runs: { ...s.runs, [runID]: { ...current, title } } }
    }),
  applyRunProtected: (runID, isProtected) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current || current.protected === isProtected) return {}
      return { runs: { ...s.runs, [runID]: { ...current, protected: isProtected } } }
    }),
  applyRunArchived: (runID, archivedAt, deletesAt) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current) return {}
      const next = {
        ...current,
        archived_at: archivedAt ?? undefined,
        deletes_at: deletesAt ?? undefined,
      }
      if (current.archived_at === next.archived_at && current.deletes_at === next.deletes_at) {
        return {}
      }
      return { runs: { ...s.runs, [runID]: next } }
    }),
})

/**
 * Whether a run's disposition is final enough to archive: merged, abandoned,
 * failed or interrupted. A completed run still awaits a human disposition
 * (Close), so it stays off this list even though it has stopped. The archive
 * command gate and every hide guard (board selectors, sidebar selectors,
 * the attention count) share this one predicate - see "Archiving hides a
 * finished run" in docs/dashboard-frontend.md.
 */
export function isArchivable(status: RunStatus): boolean {
  return (
    status === 'merged' ||
    status === 'abandoned' ||
    status === 'failed' ||
    status === 'interrupted'
  )
}

/**
 * Whether a run has stopped for good: every archivable status (above) plus
 * `completed`, which has also stopped but still awaits a human disposition
 * and so is not itself archivable. Used to freeze `finished_at` once a run's
 * outcome is settled - never for a hide guard, which wants `isArchivable`.
 */
export function isTerminal(status: RunStatus): boolean {
  return status === 'completed' || isArchivable(status)
}
