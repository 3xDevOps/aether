import type { Api } from '@/lib/api'
import type { FileDiffStat, Overlap, OverlapPeer, Run, RunDiffPayload, RunPatch } from '@/lib/types'
import type { RootStore } from '@/store'
import type { SliceCreator } from '@/store/slice'

/**
 * One `run.diff` event: the stat set of a run's worktree at the moment the
 * server took the snapshot. The patch text beside it is a diff against the
 * comparison the reader chose.
 */
export interface DiffSnapshot {
  time: string
  files: FileDiffStat[]
  truncated?: boolean
  /** The worktree's git tree at this snapshot; absent on a server that predates them. */
  tree?: string
  /** The checkout's HEAD commit at this snapshot and its subject line. */
  head?: string
  headSubject?: string
  historyGap?: boolean
  snapshotError?: string
}

export function diffSnapshot(time: string, p: RunDiffPayload): DiffSnapshot {
  return {
    time,
    files: p.files ?? [],
    truncated: p.truncated,
    tree: p.tree,
    head: p.head,
    headSubject: p.head_subject,
    historyGap: p.history_gap,
    snapshotError: p.snapshot_error,
  }
}

export function diffTotals(files: { additions: number; deletions: number }[] | undefined) {
  if (!files) return undefined
  let additions = 0
  let deletions = 0
  for (const file of files) {
    additions += file.additions
    deletions += file.deletions
  }
  return { additions, deletions }
}

/**
 * What the Compare menu keeps of a snapshot: its tree and the numbers its row
 * shows, never the file list, so a long run's history stays small.
 */
export interface Checkpoint {
  time: string
  tree?: string
  head?: string
  headSubject?: string
  files: number
  additions: number
  deletions: number
  truncated?: boolean
  historyGap?: boolean
  snapshotError?: string
}

export function checkpointOf(snapshot: DiffSnapshot): Checkpoint {
  const { additions, deletions } = diffTotals(snapshot.files)!
  return {
    time: snapshot.time,
    tree: snapshot.tree,
    head: snapshot.head,
    headSubject: snapshot.headSubject,
    files: snapshot.files.length,
    additions,
    deletions,
    truncated: snapshot.truncated,
    historyGap: snapshot.historyGap,
    snapshotError: snapshot.snapshotError,
  }
}

/**
 * What the Changes view diffs the run's current state against: its effective
 * base, the tree it was forked from, or a recorded checkpoint.
 */
export type Comparison = { kind: 'all' } | { kind: 'start' } | { kind: 'since'; checkpoint: Checkpoint }

/** The `from` tree the patch endpoint takes for `comparison`; '' for all changes. */
export function comparisonFrom(comparison: Comparison, forkTree: string): string {
  if (comparison.kind === 'start') return forkTree
  return comparison.kind === 'since' ? (comparison.checkpoint.tree ?? '') : ''
}

/** Review comments belong to the comparison they were written on: '' for all changes. */
export function comparisonScope(comparison: Comparison): string {
  if (comparison.kind === 'start') return 'start'
  return comparison.kind === 'since' ? `since:${comparison.checkpoint.tree}` : ''
}

/** Newest first, one entry per tree, the newest for a tree a run returned to;
 * snapshots without a tree are told apart by time. */
function mergeCheckpoints(known: Checkpoint[], incoming: Checkpoint[]): Checkpoint[] {
  const seen = new Set<string>()
  return [...known, ...incoming]
    .sort((a, b) => Date.parse(b.time) - Date.parse(a.time))
    .filter((entry) => {
      const key = entry.tree ?? `at ${entry.time}`
      if (seen.has(key)) return false
      seen.add(key)
      return true
    })
}

/** What the Changes view knows about one run. */
export interface RunDiffState {
  /** The commit the all-changes patch is measured against, as the server last said. */
  base: string
  /** The tree of the commit the run forked from, which "since the run started" diffs against. */
  forkTree: string
  patch: string
  /**
   * The comparison the patch, and the status and error below, answer for:
   * '' for all changes, else the snapshot tree the patch starts at.
   */
  from: string
  truncated: boolean
  recorded: boolean
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
  /** Live snapshots with their file lists, newest first. */
  snapshots: DiffSnapshot[]
  /** Every recorded snapshot the Compare menu offers, newest first: history merged with live ones. */
  checkpoints: Checkpoint[]
  /** Set once history has been read; `before` is the cursor of the next older page. */
  history?: { more: boolean; before?: number }
  historyError?: string
  /**
   * The newest `run.diff` in the run's history, read while this page has
   * seen none: `undefined` until answered, `null` when the run has none. Any
   * live snapshot is newer.
   */
  persisted?: DiffSnapshot | null
  persistedError?: string
}

export const initialDiff: RunDiffState = {
  base: '',
  forkTree: '',
  patch: '',
  from: '',
  truncated: false,
  recorded: false,
  status: 'loading',
  revision: 0,
  fetched: -1,
  snapshots: [],
  checkpoints: [],
}

const maxSnapshots = 40

export interface DiffSlice {
  diffs: Record<string, RunDiffState>
  /** Runs whose Session changes bar is expanded. */
  changesOpen: Record<string, true>
  toggleChanges: (runID: string) => void
  overlaps: Record<string, OverlapPeer[]>
  setDiff: (runID: string, patch: Partial<RunDiffState>) => void
  applyPatch: (patch: RunPatch, revision: number, from: string) => void
  applyCheckpoints: (runID: string, checkpoints: Checkpoint[], history: { more: boolean; before?: number }) => void
  noteDiffSnapshot: (runID: string, snapshot: DiffSnapshot) => void
  refreshDiff: (runID: string) => void
  setOverlaps: (overlaps: Overlap[]) => void
  applyOverlap: (runID: string, peers: OverlapPeer[]) => void
}

export const createDiffSlice: SliceCreator<DiffSlice> = (set, get) => ({
  diffs: {},
  changesOpen: {},
  toggleChanges: (runID) =>
    set((s) => {
      const changesOpen = { ...s.changesOpen }
      if (changesOpen[runID]) delete changesOpen[runID]
      else changesOpen[runID] = true
      return { changesOpen }
    }),
  overlaps: {},
  setDiff: (runID, patch) =>
    set((s) => ({
      diffs: { ...s.diffs, [runID]: { ...(s.diffs[runID] ?? initialDiff), ...patch } },
    })),
  applyPatch: (patch, revision, from) =>
    set((s) => {
      const current = s.diffs[patch.run_id] ?? initialDiff
      return {
        diffs: {
          ...s.diffs,
          [patch.run_id]: {
            ...current,
            // A patch that starts at a snapshot names that snapshot as its base.
            base: from === '' ? patch.base : current.base,
            forkTree: patch.fork_tree ?? current.forkTree,
            patch: patch.patch,
            from,
            truncated: patch.truncated,
            recorded: patch.recorded ?? false,
            status: 'ready',
            error: undefined,
            fetched: revision,
          },
        },
      }
    }),
  applyCheckpoints: (runID, checkpoints, history) =>
    set((s) => {
      const current = s.diffs[runID] ?? initialDiff
      return {
        diffs: {
          ...s.diffs,
          [runID]: {
            ...current,
            checkpoints: mergeCheckpoints(current.checkpoints, checkpoints),
            history,
            historyError: undefined,
          },
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
            checkpoints: mergeCheckpoints(current.checkpoints, [checkpointOf(snapshot)]),
          },
        },
      }
    })
  },
  // Refresh asks again by declaring the current patch one revision behind.
  refreshDiff: (runID) =>
    set((s) => {
      const current = s.diffs[runID] ?? initialDiff
      return { diffs: { ...s.diffs, [runID]: { ...current, fetched: current.revision - 1 } } }
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

/** Runs with a read in flight, and whether a reconnect asked for another after it. */
const reading = new Map<string, boolean>()

/** Reads the newest persisted `run.diff`, which a page load has not seen.
 * `fresh` asks for an answer newer than any read already in flight. */
export async function readPersistedDiff(store: RootStore, client: Api, run: Pick<Run, 'id' | 'workspace_id'>, fresh = false): Promise<void> {
  if (reading.has(run.id)) {
    if (fresh) reading.set(run.id, true)
    return
  }
  reading.set(run.id, false)
  try {
    const page = await client.workspaceTimeline({ workspace_id: run.workspace_id, run_id: run.id, types: ['run.diff'], newest: true, limit: 1 })
    const ev = page.events.at(-1)
    store.getState().setDiff(run.id, { persisted: ev ? diffSnapshot(ev.time, ev.payload as RunDiffPayload) : null, persistedError: undefined })
  } catch (err) {
    store.getState().setDiff(run.id, { persistedError: err instanceof Error ? err.message : String(err) })
  } finally {
    const again = reading.get(run.id)
    reading.delete(run.id)
    if (again) void readPersistedDiff(store, client, run)
  }
}

/** Runs with a history page in flight. */
const paging = new Set<string>()

const checkpointPage = 50

/**
 * Reads one page of the run's recorded snapshots: the newest, or with `older`
 * the page before the oldest already read.
 */
export async function readCheckpoints(store: RootStore, client: Api, run: Pick<Run, 'id' | 'workspace_id'>, older = false): Promise<void> {
  const before = store.getState().diffs[run.id]?.history?.before
  if (paging.has(run.id) || (older && !before)) return
  paging.add(run.id)
  try {
    const page = await client.workspaceTimeline({
      workspace_id: run.workspace_id,
      run_id: run.id,
      types: ['run.diff'],
      newest: true,
      limit: checkpointPage,
      ...(older && { before_seq: before }),
    })
    const read = page.events.map((ev) => checkpointOf(diffSnapshot(ev.time, ev.payload as RunDiffPayload)))
    store.getState().applyCheckpoints(run.id, read, { more: page.more, before: page.older_seq })
  } catch (err) {
    store.getState().setDiff(run.id, { historyError: err instanceof Error ? err.message : String(err) })
  } finally {
    paging.delete(run.id)
  }
}
