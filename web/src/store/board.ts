import type { RunStatus } from '@/lib/types'
import type { RunRecord } from '@/store/runs'
import type { SliceCreator } from '@/store/slice'

/**
 * What was acknowledged: the state the run was in, not just when it changed.
 * `stateChangedAt` is recomputed from the run's timestamps on every fetch and
 * can move backwards - a needs-attention run has no `finished_at`, so a
 * re-hydration falls back to `started_at` - which a time-only comparison would
 * read as "older than the ack" and mute the card that needed the human.
 */
export interface Ack {
  status: RunStatus
  at: string
}

export interface BoardSlice {
  acked: Record<string, Ack>
  /**
   * Run ID to paused. The domain status enum has no paused state - a paused
   * run still reads `running` - so this comes from the pause and resume
   * timeline events, seeded at hydration from the run list's `paused` wire
   * field. A run missing from the map is *unknown*, not running: a legacy
   * server sends no `paused` field, so a run paused before the tab loaded
   * looks the same as one that was never paused.
   */
  pausedRuns: Record<string, boolean>
  ackRun: (runID: string) => void
  ackAll: () => void
  setPaused: (runID: string, paused: boolean) => void
  /** Replaces the map wholesale; the hydration snapshot is authoritative. */
  seedPaused: (entries: Record<string, boolean>) => void
}

const ackOf = (run: RunRecord): Ack => ({
  status: run.status,
  at: run.stateChangedAt,
})

export const createBoardSlice: SliceCreator<BoardSlice> = (set) => ({
  acked: {},
  pausedRuns: {},
  ackRun: (runID) =>
    set((s) => {
      const run = s.runs[runID]
      return run ? { acked: { ...s.acked, [runID]: ackOf(run) } } : {}
    }),
  ackAll: () =>
    set((s) => ({
      acked: Object.fromEntries(Object.values(s.runs).map((r) => [r.id, ackOf(r)])),
    })),
  setPaused: (runID, paused) =>
    set((s) => ({ pausedRuns: { ...s.pausedRuns, [runID]: paused } })),
  seedPaused: (pausedRuns) => set({ pausedRuns }),
})

/**
 * A run is unseen until someone acknowledges the state it is in now. Any
 * difference from the acknowledged state counts, in either direction. Acks are
 * app-wide, so the sidebar row and the board card mute together.
 */
export function isUnseen(acked: Record<string, Ack>, run: RunRecord): boolean {
  const ack = acked[run.id]
  return !ack || ack.status !== run.status || ack.at !== run.stateChangedAt
}

/** Reads a workspace.timeline payload as a pause state change, or null. */
export function pausedFromTimeline(payload: unknown): boolean | null {
  const kind = (payload as { kind?: string } | null)?.kind
  if (kind === 'pause') return true
  if (kind === 'resume') return false
  return null
}
