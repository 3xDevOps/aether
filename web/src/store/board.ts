import type { SliceCreator } from '@/store/slice'

export interface BoardSlice {
  /**
   * Run ID to paused. The domain status enum has no paused state - a paused
   * run still reads `running` - so this comes from the pause and resume
   * timeline events, seeded at hydration from the run list's `paused` wire
   * field. A run missing from the map is *unknown*, not running: a legacy
   * server sends no `paused` field, so a run paused before the tab loaded
   * looks the same as one that was never paused.
   */
  pausedRuns: Record<string, boolean>
  setPaused: (runID: string, paused: boolean) => void
  /** Replaces the map wholesale; the hydration snapshot is authoritative. */
  seedPaused: (entries: Record<string, boolean>) => void
}

export const createBoardSlice: SliceCreator<BoardSlice> = (set) => ({
  pausedRuns: {},
  setPaused: (runID, paused) =>
    set((s) => ({ pausedRuns: { ...s.pausedRuns, [runID]: paused } })),
  seedPaused: (pausedRuns) => set({ pausedRuns }),
})

/** Reads a workspace.timeline payload as a pause state change, or null. */
export function pausedFromTimeline(payload: unknown): boolean | null {
  const kind = (payload as { kind?: string } | null)?.kind
  if (kind === 'pause') return true
  if (kind === 'resume') return false
  return null
}
