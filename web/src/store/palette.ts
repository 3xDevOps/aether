import type { SliceCreator } from '@/store/slice'

/** The palette's forms, each needing input the palette cannot take. */
export type PaletteDialog = 'launch' | 'inject' | 'forward' | 'close'

export interface PaletteSlice {
  paletteOpen: boolean
  paletteDialog: PaletteDialog | null
  /** The run a form acts on; the launch form has none. */
  paletteRunID: string | null
  togglePalette: (open?: boolean) => void
  openPaletteDialog: (dialog: PaletteDialog, runID?: string) => void
  closePaletteDialog: () => void
}

export const createPaletteSlice: SliceCreator<PaletteSlice> = (set) => ({
  paletteOpen: false,
  paletteDialog: null,
  paletteRunID: null,
  togglePalette: (open) => set((s) => ({ paletteOpen: open ?? !s.paletteOpen })),
  openPaletteDialog: (dialog, runID) =>
    set({ paletteOpen: false, paletteDialog: dialog, paletteRunID: runID ?? null }),
  closePaletteDialog: () => set({ paletteDialog: null, paletteRunID: null }),
})
