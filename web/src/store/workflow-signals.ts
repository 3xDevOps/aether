import type { RootState } from '@/store'
import type { SliceCreator } from '@/store/slice'

/**
 * Per-run counts of `git.branch` and `run.pr` events and per-mission counts of
 * `mission.changed` events. The events are hints, so
 * a view keys its re-read on a count rather than rendering the payload.
 */
export interface WorkflowSignalsSlice {
  gitSeq: Record<string, number>
  prSeq: Record<string, number>
  missionSeq: Record<string, number>
  bumpWorkflowSignal: (signal: WorkflowSignal, id: string) => void
}

export type WorkflowSignal = 'gitSeq' | 'prSeq' | 'missionSeq'

export const createWorkflowSignalsSlice: SliceCreator<WorkflowSignalsSlice> = (set) => ({
  gitSeq: {},
  prSeq: {},
  missionSeq: {},
  bumpWorkflowSignal: (signal, id) =>
    set((s) => ({ [signal]: { ...s[signal], [id]: (s[signal][id] ?? 0) + 1 } })),
})

export const gitSeqOf = (runID: string) => (s: RootState) => s.gitSeq[runID] ?? 0
export const prSeqOf = (runID: string) => (s: RootState) => s.prSeq[runID] ?? 0
export const missionSeqOf = (missionID: string) => (s: RootState) => s.missionSeq[missionID] ?? 0
