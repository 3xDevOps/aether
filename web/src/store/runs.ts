import type { Run, RunInputRequest, RunReviewPayload, RunStatus, RunStatusPayload } from '@/lib/types'
import { nextActivity, type AgentPayload, type ToolActivity } from '@/store/activity'
import type { SliceCreator } from '@/store/slice'

/** A run plus its last execution-status change time, for sorting and acknowledgments. */
export type RunRecord = Omit<Run, 'status_changed_at'> & {
  reason?: string
  /** The wire's `status_changed_at`, then kept current by `run.status` events. */
  stateChangedAt: string
  /** The snapshot had no `status_changed_at`, so this is the run's finish, start or creation. */
  stateChangedAtEstimated: boolean
  /** A pending kill's prediction wrote `stateChangedAt`, so the server's event replaces it. */
  stateChangedAtPredicted?: boolean
  /** Client-side only: what the agent did last, from `run.agent` events. */
  toolActivity?: ToolActivity
}

export function toRecord(snapshot: Run, previous?: RunRecord): RunRecord {
  // A snapshot read before an applied status event must not undo what that event set.
  const run: Run = previous && snapshot.status_seq < previous.status_seq
    ? {
        ...snapshot,
        status: previous.status,
        reason: previous.reason,
        state: previous.state,
        cause: previous.cause,
        wait: previous.wait,
        status_seq: previous.status_seq,
        outcome_unseen: previous.outcome_unseen,
        finish_unopened: previous.finish_unopened,
        review: previous.review,
        review_changed_at: previous.review_changed_at,
        finished_at: previous.finished_at,
        started_at: previous.started_at,
        status_changed_at: undefined,
      }
    : previous && snapshot.status_seq === previous.status_seq
      // Only a status change sets an outcome flag; a read from before the owner opened the run must not set it again.
      ? {
          ...snapshot,
          outcome_unseen: snapshot.outcome_unseen === true && previous.outcome_unseen === true,
          finish_unopened: snapshot.finish_unopened === true && previous.finish_unopened === true,
        }
      : snapshot
  const { status_changed_at: snapshotAt, ...wire } = run
  const carry = previous && previous.status === run.status
  // A snapshot can be older than the events already applied, so it never moves
  // a same-status run's change time backwards. The wire has whole seconds and
  // an event a finer time: they compare as instants, not as strings.
  const behind =
    carry &&
    !previous.stateChangedAtEstimated &&
    snapshotAt !== undefined &&
    Date.parse(snapshotAt) < Date.parse(previous.stateChangedAt)
  const changedAt = behind ? undefined : snapshotAt
  const record: RunRecord = {
    ...wire,
    reason: run.reason ?? (carry ? previous.reason : undefined),
    stateChangedAt:
      changedAt ??
      (carry ? previous.stateChangedAt : (run.finished_at ?? run.started_at ?? run.created_at)),
    stateChangedAtEstimated: !changedAt && (carry ? previous.stateChangedAtEstimated : !run.finished_at),
  }
  // No snapshot carries tool activity, so a re-read must not erase it.
  if (carry && previous.toolActivity) record.toolActivity = previous.toolActivity
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

export interface PredictionMarker {
  waiting?: boolean
}

export interface RunsSlice {
  runs: Record<string, RunRecord>
  /** Counts `setRuns` calls, so a view can tell a snapshot re-read from a live event. */
  runSnapshots: number
  setRuns: (runs: Run[]) => void
  upsertRun: (run: Run) => void
  removeRun: (runID: string) => void
  /** Merges a verb's predicted fields; see "Actions" in docs/dashboard-frontend.md. */
  patchRun: (runID: string, fields: Partial<RunRecord>) => void
  /**
   * Runs with a predicted verb awaiting its answer, each held by that verb's
   * marker; `waiting` marks a slow verb past the deadline. See "Actions" in
   * docs/dashboard-frontend.md.
   */
  predicting: Record<string, PredictionMarker>
  /** Clearing leaves alone a lock another verb's marker holds. */
  setPredicting: (runID: string, marker: PredictionMarker, pending: boolean) => void
  /**
   * Runs a pending Delete took off screen, each with the record to put back
   * if the server refuses. A snapshot or event re-read cannot bring one back
   * meanwhile. See "Commands and the palette" in docs/dashboard-frontend.md.
   */
  deletingRuns: Record<string, RunRecord>
  /** Putting back leaves a run re-added since in place; `removeRun` drops the held record. */
  setDeleting: (runID: string, deleting: boolean) => void
  applyRunInput: (runID: string, requests: RunInputRequest[]) => void
  /** Ignores an event whose `status_seq` is not past the held one. */
  applyRunStatus: (runID: string, payload: RunStatusPayload, time: string) => void
  /** Ignores an event whose `status_seq` is not past the held one, as `applyRunStatus` does. */
  applyRunReview: (runID: string, payload: RunReviewPayload, time: string) => void
  /** Clears `outcome_unseen`: the owner has opened the run. */
  applyOutcomeSeen: (runID: string) => void
  /** Clears `finish_unopened`: a member has opened the run. */
  applyFinishOpened: (runID: string) => void
  applyLastCommit: (runID: string, commit: string, time: string) => void
  applyAgentEvent: (runID: string, payload: AgentPayload, time: string) => void
  applyRunTitle: (runID: string, title: string) => void
  applyRunModel: (runID: string, model: string) => void
  applyRunEffort: (runID: string, effort: string) => void
  applyRunProtected: (runID: string, isProtected: boolean) => void
  applyRunController: (runID: string, memberID: string, lastMemberID?: string) => void
  applyRunAccount: (runID: string, accountID: string, requestedID?: string) => void
  applyRunArchived: (
    runID: string,
    archivedAt: string | null,
  ) => void
}

export const createRunsSlice: SliceCreator<RunsSlice> = (set) => ({
  runs: {},
  runSnapshots: 0,
  setRuns: (runs) =>
    set((s) => {
      const records = Object.fromEntries(
        runs.filter((r) => !s.deletingRuns[r.id]).map((r) => [r.id, toRecord(r, s.runs[r.id])]),
      )
      const listed = (runID: string) => runID in records || runID in s.deletingRuns
      return {
        runs: records,
        runSnapshots: s.runSnapshots + 1,
        terminalWriteIntents: pruneRuns(s.terminalWriteIntents, listed),
        terminalControlSessions: pruneRuns(s.terminalControlSessions, listed),
      }
    }),
  upsertRun: (run) =>
    set((s) => {
      if (s.deletingRuns[run.id]) return {}
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
        deletingRuns: pruneRuns(s.deletingRuns, other),
        terminalWriteIntents: pruneRuns(s.terminalWriteIntents, other),
        terminalControlSessions: pruneRuns(s.terminalControlSessions, other),
      }
    }),
  patchRun: (runID, fields) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current) return {}
      return { runs: { ...s.runs, [runID]: { ...current, ...fields } } }
    }),
  predicting: {},
  setPredicting: (runID, marker, pending) =>
    set((s) => {
      if (pending) return { predicting: { ...s.predicting, [runID]: marker } }
      return s.predicting[runID] === marker ? { predicting: pruneRuns(s.predicting, (id) => id !== runID) } : {}
    }),
  deletingRuns: {},
  setDeleting: (runID, deleting) =>
    set((s) => {
      const other = (id: string) => id !== runID
      if (deleting) {
        const current = s.runs[runID]
        return current ? { runs: pruneRuns(s.runs, other), deletingRuns: { ...s.deletingRuns, [runID]: current } } : {}
      }
      const held = s.deletingRuns[runID]
      if (!held) return {}
      return {
        runs: s.runs[runID] ? s.runs : { ...s.runs, [runID]: held },
        deletingRuns: pruneRuns(s.deletingRuns, other),
      }
    }),
  applyRunStatus: (runID, { to, reason, state, cause, wait, status_seq, outcome_unseen = false, finish_unopened = false, review = '', review_changed_at }, time) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current || status_seq <= current.status_seq) return {}
      // The server moves `status_changed_at` on the same changes and no others.
      const changed = to !== current.status || (to === 'needs-attention' && reason !== current.reason)
      const timed = changed || current.stateChangedAtPredicted
      const next: RunRecord = {
        ...current,
        status: to,
        reason,
        state,
        cause,
        wait,
        status_seq,
        stateChangedAt: timed ? time : current.stateChangedAt,
        stateChangedAtEstimated: !timed && current.stateChangedAtEstimated,
        outcome_unseen,
        finish_unopened,
        review,
        review_changed_at: review_changed_at ?? (review === (current.review ?? '') ? current.review_changed_at : time),
      }
      delete next.stateChangedAtPredicted
      if (to !== current.status) delete next.toolActivity
      if (to === 'running' && (!next.started_at || isTerminal(current.status))) next.started_at = time
      if (isTerminal(to)) {
        next.finished_at = time
        next.pending_inputs = []
      }
      if (!isTerminal(to)) {
        next.finished_at = null
        delete next.container_retained_until
        delete next.cleanup_pending
        delete next.cleanup_error
      }
      return { runs: { ...s.runs, [runID]: next } }
    }),
  applyRunReview: (runID, { review, review_changed_at, status_seq }, time) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current || status_seq <= current.status_seq) return {}
      return { runs: { ...s.runs, [runID]: { ...current, review, review_changed_at: review_changed_at ?? time, status_seq } } }
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
      const toolActivity = nextActivity(current.toolActivity, payload, time)
      if (toolActivity === current.toolActivity) return {}
      return { runs: { ...s.runs, [runID]: { ...current, toolActivity } } }
    }),

  applyRunTitle: (runID, title) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current || current.title === title) return {}
      return { runs: { ...s.runs, [runID]: { ...current, title } } }
    }),
  applyRunModel: (runID, model) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current || current.model === model) return {}
      return { runs: { ...s.runs, [runID]: { ...current, model } } }
    }),
  applyRunEffort: (runID, effort) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current || current.effort === effort) return {}
      return { runs: { ...s.runs, [runID]: { ...current, effort } } }
    }),
  applyRunProtected: (runID, isProtected) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current || current.protected === isProtected) return {}
      return { runs: { ...s.runs, [runID]: { ...current, protected: isProtected } } }
    }),
  applyRunController: (runID, memberID, lastMemberID) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current || (current.controller_member_id === memberID && current.last_controller_member_id === lastMemberID)) return {}
      return {
        runs: {
          ...s.runs,
          [runID]: { ...current, controller_member_id: memberID, last_controller_member_id: lastMemberID },
        },
      }
    }),
  applyRunAccount: (runID, accountID, requestedID) =>
    set((s) => {
      const current = s.runs[runID]
      if (!current) return {}
      return {
        runs: {
          ...s.runs,
          [runID]: { ...current, account_member_id: accountID, requested_account_member_id: requestedID },
        },
      }
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
