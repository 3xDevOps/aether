import type { FileDiffStat, Overlap, OverlapPeer, RunPatch } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

/**
 * One `run.diff` event: the stat set of a run's worktree at the moment the
 * server took the snapshot, plus the trees bounding the interval it ended.
 * The list is the timeline - what changed, and when - and the patch text
 * beside it is the current diff against the fork point.
 */
export interface DiffSnapshot {
  time: string
  files: FileDiffStat[]
  /** The worktree's git tree at this snapshot. */
  tree?: string
  /**
   * The tree the previous snapshot ended at, or the fork point for the first.
   * Both are absent on a server that predates per-snapshot trees, and the
   * interval such a snapshot ended cannot be shown.
   */
  parentTree?: string
}

/**
 * The diff of one interval: `from` tree to `to` tree. It is addressed by two
 * tree ids, so its text can never go stale and is fetched once.
 */
export interface IntervalPatch {
  patch: string
  truncated: boolean
  /** Loaded, loading, or the message from the attempt that failed. */
  status: 'loading' | 'ready' | 'error'
  error?: string
}

/** The cache key for one interval. */
export function intervalKey(from: string, to: string): string {
  return `${from}..${to}`
}

/** The cached intervals the given snapshots can still ask for. */
function keptIntervals(
  intervals: Record<string, IntervalPatch>,
  snapshots: DiffSnapshot[],
): Record<string, IntervalPatch> {
  const live = new Set<string>()
  for (const snap of snapshots) {
    if (snap.tree && snap.parentTree) live.add(intervalKey(snap.parentTree, snap.tree))
  }
  return Object.fromEntries(Object.entries(intervals).filter(([key]) => live.has(key)))
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
   * Interval patches by `intervalKey`, pruned to the keys the surviving
   * snapshots name whenever the list is trimmed, so the cache stays bounded
   * by `maxSnapshots` rather than by how long the run has been open.
   */
  intervals: Record<string, IntervalPatch>
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
  intervals: {},
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
  setIntervalPatch: (runID: string, key: string, entry: IntervalPatch) => void
  noteDiffSnapshot: (runID: string, snapshot: DiffSnapshot) => void
  refreshDiff: (runID: string) => void
  setOverlaps: (overlaps: Overlap[]) => void
  applyOverlap: (runID: string, peers: OverlapPeer[]) => void
}

export const createDiffSlice: SliceCreator<DiffSlice> = (set, get) => ({
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
  // An interval patch answers for two tree ids, so it is written once and
  // never invalidated: the cumulative patch's revision machinery does not
  // apply to it.
  setIntervalPatch: (runID, key, entry) =>
    set((s) => {
      const current = s.diffs[runID] ?? initialDiff
      return {
        diffs: {
          ...s.diffs,
          [runID]: { ...current, intervals: { ...current.intervals, [key]: entry } },
        },
      }
    }),
  noteDiffSnapshot: (runID, snapshot) => {
    get().invalidateRun(runID)
    set((s) => {
      const current = s.diffs[runID] ?? initialDiff
      const snapshots = [snapshot, ...current.snapshots].slice(0, maxSnapshots)
      return {
        diffs: {
          ...s.diffs,
          [runID]: {
            ...current,
            revision: current.revision + 1,
            snapshots,
            // A snapshot that fell off the end can never be selected again,
            // so its patch is dead weight: a long run would otherwise
            // accumulate every interval it ever rendered.
            intervals: keptIntervals(current.intervals, snapshots),
          },
        },
      }
    })
  },
  // Refresh asks again by declaring the current patch one revision behind.
  // Failed intervals are dropped with it: an interval that answered is
  // immutable and stays, but a cached failure would otherwise be the one
  // thing on this tab that no button can retry.
  refreshDiff: (runID) =>
    set((s) => {
      const current = s.diffs[runID] ?? initialDiff
      const intervals = Object.fromEntries(
        Object.entries(current.intervals).filter(([, entry]) => entry.status !== 'error'),
      )
      return {
        diffs: {
          ...s.diffs,
          [runID]: { ...current, fetched: current.revision - 1, intervals },
        },
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
