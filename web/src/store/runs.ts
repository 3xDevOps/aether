import type { Run, RunInputRequest, RunStatus } from '@/lib/types'
import { nextActivity, type AgentPayload, type RunActivity } from '@/store/activity'
import type { SliceCreator } from '@/store/slice'

/** A run plus its last execution-status change time, for sorting and acknowledgments. */
export type RunRecord = Run & {
  reason?: string
  stateChangedAt: string
  /** The wire has no status-change time, so a snapshot falls back to the run's start. */
  stateChangedAtEstimated: boolean
  /** Client-side only: what the agent did last, from `run.agent` events. */
  activity?: RunActivity
}

export function toRecord(run: Run, previous?: RunRecord): RunRecord {
  const carry = previous && previous.status === run.status
  const record: RunRecord = {
    ...run,
    reason: run.reason ?? (carry ? previous.reason : undefined),
    stateChangedAt: carry
      ? previous.stateChangedAt
      : (run.finished_at ?? run.started_at ?? run.created_at),
    stateChangedAtEstimated: carry ? previous.stateChangedAtEstimated : !run.finished_at,
  }
  // No snapshot carries activity, so a re-read must not erase it.
  if (carry && previous.activity) record.activity = previous.activity
  return record
}

/** `records` without the runs `keep` rejects; the same object when none is. */
export function pruneRuns<T>(
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
    outcomeUnseen?: boolean,
    finishUnopened?: boolean,
  ) => void
  /** Clears `outcome_unseen`: the owner has opened the run. */
  applyOutcomeSeen: (runID: string) => void
  /** Clears `finish_unopened`: a member has opened the run. */
  applyFinishOpened: (runID: string) => void
  applyLastCommit: (runID: string, commit: string, time: string) => void
  applyAgentEvent: (runID: string, payload: AgentPayload, time: string) => void
  applyRunTitle: (runID: string, title: string) => void
  applyUnackedMessages: (runID: string, unread: Pick<Run, 'unacked_messages' | 'oldest_unacked_at'>) => void
  applyRunProtected: (runID: string, isProtected: boolean) => void
  applyRunController: (runID: string, memberID: string) => void
  applyRunArchived: (
    runID: string,
    archivedAt: string | null,
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
      // A late route/launch snapshot must not resurrect a resolved request.
      const next = toRecord(run, current)
      next.pending_inputs = isTerminal(next.status) ? [] : current?.pending_inputs ?? run.pending_inputs
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
  applyRunStatus: (runID, to, reason, time, outcomeUnseen = false, finishUnopened = false) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current) return {}
      const next: RunRecord = {
        ...current,
        status: to,
        reason,
        stateChangedAt: time,
        stateChangedAtEstimated: false,
        outcome_unseen: outcomeUnseen,
        finish_unopened: finishUnopened,
      }
      if (to !== current.status) delete next.activity
      if (to === 'running' && !next.started_at) next.started_at = time
      if (isTerminal(to)) {
        next.finished_at = time
        next.pending_inputs = []
      }
      if (!isTerminal(to)) {
        delete next.container_retained_until
        delete next.cleanup_pending
        delete next.cleanup_error
      }
      return { runs: { ...s.runs, [runID]: next } }
    }),
  applyOutcomeSeen: (runID) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current?.outcome_unseen) return {}
      return { runs: { ...s.runs, [runID]: { ...current, outcome_unseen: false } } }
    }),
  applyFinishOpened: (runID) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current?.finish_unopened) return {}
      return { runs: { ...s.runs, [runID]: { ...current, finish_unopened: false } } }
    }),

  applyRunInput: (runID, requests) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current || isTerminal(current.status)) return {}
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

  applyAgentEvent: (runID, payload, time) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current) return {}
      const activity = nextActivity(current.activity, payload, time)
      if (activity === current.activity) return {}
      return { runs: { ...s.runs, [runID]: { ...current, activity } } }
    }),

  applyRunTitle: (runID, title) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current || current.title === title) return {}
      return { runs: { ...s.runs, [runID]: { ...current, title } } }
    }),
  applyUnackedMessages: (runID, { unacked_messages, oldest_unacked_at }) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current || (current.unacked_messages === unacked_messages && current.oldest_unacked_at === oldest_unacked_at)) return {}
      return { runs: { ...s.runs, [runID]: { ...current, unacked_messages, oldest_unacked_at } } }
    }),
  applyRunProtected: (runID, isProtected) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current || current.protected === isProtected) return {}
      return { runs: { ...s.runs, [runID]: { ...current, protected: isProtected } } }
    }),
  applyRunController: (runID, memberID) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current || current.controller_member_id === memberID) return {}
      return { runs: { ...s.runs, [runID]: { ...current, controller_member_id: memberID } } }
    }),
  applyRunArchived: (runID, archivedAt) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current) return {}
      const next = {
        ...current,
        archived_at: archivedAt ?? undefined,
      }
      if (current.archived_at === next.archived_at) {
        return {}
      }
      return { runs: { ...s.runs, [runID]: next } }
    }),
})

/**
 * A completed run still awaits Close, so it is not archivable. The archive gate
 * and every hide guard share this predicate; see docs/dashboard-frontend.md.
 */
export function isArchivable(status: RunStatus): boolean {
  return (
    status === 'merged' ||
    status === 'abandoned' ||
    status === 'failed' ||
    status === 'interrupted'
  )
}

/** Freezes `finished_at`; never a hide guard, which wants `isArchivable`. */
export function isTerminal(status: RunStatus): boolean {
  return status === 'completed' || isArchivable(status)
}
