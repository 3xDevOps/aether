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
  applyRunStatus: (
    runID: string,
    to: RunStatus,
    reason: string | undefined,
    time: string,
  ) => void
  applyRunTitle: (runID: string, title: string) => void
}

export const createRunsSlice: SliceCreator<RunsSlice> = (set) => ({
  runs: {},
  setRuns: (runs) =>
    set((s) => ({
      runs: Object.fromEntries(
        runs.map((r) => [r.id, toRecord(r, s.runs[r.id])]),
      ),
    })),
  upsertRun: (run) =>
    set((s) => ({ runs: { ...s.runs, [run.id]: toRecord(run, s.runs[run.id]) } })),
  applyRunStatus: (runID, to, reason, time) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current) return {}
      const next: RunRecord = { ...current, status: to, reason, stateChangedAt: time }
      if (to === 'running' && !next.started_at) next.started_at = time
      if (isTerminal(to)) next.finished_at = time
      return { runs: { ...s.runs, [runID]: next } }
    }),
  applyRunTitle: (runID, title) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current || current.title === title) return {}
      return { runs: { ...s.runs, [runID]: { ...current, title } } }
    }),
})

function isTerminal(status: RunStatus): boolean {
  return (
    status === 'merged' ||
    status === 'abandoned' ||
    status === 'failed' ||
    status === 'interrupted'
  )
}
