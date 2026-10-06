import type { SliceCreator } from '@/store/slice'

export interface BoardSlice {
  /**
   * Run ID to paused. A paused run's status still reads `running`, so this
   * comes from pause/resume timeline events. A missing run is unknown, not
   * running: a legacy server sends no `paused` field.
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
