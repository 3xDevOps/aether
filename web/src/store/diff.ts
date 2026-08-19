import type { FileDiffStat, Overlap, OverlapPeer, RunPatch } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

/**
 * One `run.diff` event: the stat set of a run's worktree at the moment the
 * server took the snapshot. The server keeps no patch history, so this list
 * is the timeline - what changed, and when - and the patch text beside it is
 * always the current diff against the fork point.
 */
export interface DiffSnapshot {
  time: string
  files: FileDiffStat[]
}

/** What the Diff tab knows about one run. */
export interface RunDiffState {
  base: string
  patch: string
  truncated: boolean
  /** Loaded, loading, or the message from the attempt that failed. */
  status: 'loading' | 'ready' | 'error'
  error?: string
  /**
   * Bumped by every snapshot. The patch is behind whenever this has moved
   * past `fetched`, which is what a counter buys over a stale flag: a
   * snapshot that lands while the request is in flight still shows up as a
   * gap when the answer arrives, instead of being written over.
   */
  revision: number
  /** The revision the patch, or the error, answers for; -1 before the first. */
  fetched: number
  snapshots: DiffSnapshot[]
}

export const initialDiff: RunDiffState = {
  base: '',
  patch: '',
  truncated: false,
  status: 'loading',
  revision: 0,
  fetched: -1,
  snapshots: [],
}

/**
 * How many snapshots a run keeps. The timeline answers "what changed in the
 * last few minutes", not "everything since the run began", and the list is
 * the only thing holding them - a reload starts over, because the server has
 * no history to replay.
 */
const maxSnapshots = 40

export interface DiffSlice {
  diffs: Record<string, RunDiffState>
  overlaps: Record<string, OverlapPeer[]>
  setDiff: (runID: string, patch: Partial<RunDiffState>) => void
  applyPatch: (patch: RunPatch, revision: number) => void
  noteDiffSnapshot: (runID: string, snapshot: DiffSnapshot) => void
  refreshDiff: (runID: string) => void
  setOverlaps: (overlaps: Overlap[]) => void
  applyOverlap: (runID: string, peers: OverlapPeer[]) => void
}

export const createDiffSlice: SliceCreator<DiffSlice> = (set) => ({
  diffs: {},
  overlaps: {},
  setDiff: (runID, patch) =>
    set((s) => ({
      diffs: { ...s.diffs, [runID]: { ...(s.diffs[runID] ?? initialDiff), ...patch } },
    })),
  applyPatch: (patch, revision) =>
    set((s) => ({
      diffs: {
        ...s.diffs,
        [patch.run_id]: {
          ...(s.diffs[patch.run_id] ?? initialDiff),
          base: patch.base,
          patch: patch.patch,
          truncated: patch.truncated,
          status: 'ready',
          error: undefined,
          fetched: revision,
        },
      },
    })),
  noteDiffSnapshot: (runID, snapshot) =>
    set((s) => {
      const current = s.diffs[runID] ?? initialDiff
      return {
        diffs: {
          ...s.diffs,
          [runID]: {
            ...current,
            revision: current.revision + 1,
            snapshots: [snapshot, ...current.snapshots].slice(0, maxSnapshots),
          },
        },
      }
    }),
  // Refresh asks again by declaring the current patch one revision behind.
  refreshDiff: (runID) =>
    set((s) => {
      const current = s.diffs[runID] ?? initialDiff
      return {
        diffs: { ...s.diffs, [runID]: { ...current, fetched: current.revision - 1 } },
      }
    }),
  setOverlaps: (overlaps) =>
    set(() => ({
      overlaps: Object.fromEntries(overlaps.map((o) => [o.run_id, o.with])),
    })),
  applyOverlap: (runID, peers) =>
    set((s) => {
      const next = { ...s.overlaps }
      // The radar reports a cleared overlap as an empty peer list; keeping
      // the key would leave a chip on the card with nothing in it.
      if (peers.length === 0) delete next[runID]
      else next[runID] = peers
      return { overlaps: next }
    }),
})
