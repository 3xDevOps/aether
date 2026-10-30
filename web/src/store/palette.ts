import type { WatchTarget } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

/** Every form the shell hosts; sweep this list rather than keep a copy. */
export const paletteDialogs = ['launch', 'inject', 'forward', 'close', 'clear-done', 'release-finished', 'watch'] as const

export type PaletteDialog = (typeof paletteDialogs)[number]

export interface PaletteSlice {
  paletteOpen: boolean
  paletteDialog: PaletteDialog | null
  /** Only run-specific forms carry a run id. */
  paletteRunID: string | null
  paletteForwardTarget: string | null
  /** The pull request the Watch form is for; a run's comes from paletteRunID. */
  paletteWatchTarget: WatchTarget | null
  togglePalette: (open?: boolean) => void
  openPaletteDialog: (dialog: PaletteDialog, runID?: string) => void
  openForwardDialog: (target: string) => void
  openWatchDialog: (target: WatchTarget) => void
  closePaletteDialog: () => void
}

export const createPaletteSlice: SliceCreator<PaletteSlice> = (set) => ({
  paletteOpen: false,
  paletteDialog: null,
  paletteRunID: null,
  paletteForwardTarget: null,
  paletteWatchTarget: null,
  togglePalette: (open) => set((s) => ({ paletteOpen: open ?? !s.paletteOpen })),
  openPaletteDialog: (dialog, runID) =>
    set({
      paletteOpen: false,
      paletteDialog: dialog,
      paletteRunID: runID ?? null,
      paletteForwardTarget: null,
      paletteWatchTarget: null,
    }),
  openForwardDialog: (target) =>
    set({
      paletteOpen: false,
      paletteDialog: 'forward',
      paletteRunID: null,
      paletteForwardTarget: target,
      paletteWatchTarget: null,
    }),
  openWatchDialog: (target) =>
    set({
      paletteOpen: false,
      paletteDialog: 'watch',
      paletteRunID: null,
      paletteForwardTarget: null,
      paletteWatchTarget: target,
    }),
  closePaletteDialog: () =>
    set({
      paletteDialog: null,
      paletteRunID: null,
      paletteForwardTarget: null,
      paletteWatchTarget: null,
    }),
})
