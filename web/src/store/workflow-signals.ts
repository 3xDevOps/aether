import type { SliceCreator } from '@/store/slice'

/**
 * Per-run counts of `git.branch` and `run.pr` events. The events are hints, so
 * a view keys its re-read on a count rather than rendering the payload.
 */
export interface WorkflowSignalsSlice {
  gitSeq: Record<string, number>
  prSeq: Record<string, number>
  bumpWorkflowSignal: (signal: WorkflowSignal, id: string) => void
}

export type WorkflowSignal = 'gitSeq' | 'prSeq'

export const createWorkflowSignalsSlice: SliceCreator<WorkflowSignalsSlice> = (set) => ({
  gitSeq: {},
  prSeq: {},
  bumpWorkflowSignal: (signal, id) =>
    set((s) => ({ [signal]: { ...s[signal], [id]: (s[signal][id] ?? 0) + 1 } })),
})
