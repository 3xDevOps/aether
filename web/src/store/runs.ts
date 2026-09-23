import type { Run, RunStatus } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

/**
 * A run plus the two things only the event stream knows: why it is in its
 * current status, and when it last changed. Both drive sorting and the
 * needs-attention copy in the UI.
 */
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

export interface RunsSlice {
  runs: Record<string, RunRecord>
  setRuns: (runs: Run[]) => void
  upsertRun: (run: Run) => void
  removeRun: (runID: string) => void
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
      let terminalWriteIntents = s.terminalWriteIntents
      for (const runID in s.terminalWriteIntents) {
        if (records[runID]) continue
        if (terminalWriteIntents === s.terminalWriteIntents) {
          terminalWriteIntents = { ...s.terminalWriteIntents }
        }
        delete terminalWriteIntents[runID]
      }
      return { runs: records, terminalWriteIntents }
    }),
  upsertRun: (run) =>
    set((s) => ({ runs: { ...s.runs, [run.id]: toRecord(run, s.runs[run.id]) } })),
  removeRun: (runID) =>
    set((s) => {
      const run = s.runs[runID]
      const intent = s.terminalWriteIntents[runID]
      if (!run && !intent) return {}
      if (!run) {
        const terminalWriteIntents = { ...s.terminalWriteIntents }
        delete terminalWriteIntents[runID]
        return { terminalWriteIntents }
      }
      const runs = { ...s.runs }
      delete runs[runID]
      if (!intent) return { runs }
      const terminalWriteIntents = { ...s.terminalWriteIntents }
      delete terminalWriteIntents[runID]
      return { runs, terminalWriteIntents }
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
