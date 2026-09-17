import type { ClearDonePlan } from '@/lib/commands'
import type { SliceCreator } from '@/store/slice'

/** The palette's forms, each needing input the palette cannot take. */
/** Every form the shell hosts, so a caller sweeping all of them cannot keep
 * its own list and let it drift. */
export const paletteDialogs = ['launch', 'inject', 'forward', 'close', 'clear-done'] as const

export type PaletteDialog = (typeof paletteDialogs)[number]

export interface PaletteSlice {
  paletteOpen: boolean
  paletteDialog: PaletteDialog | null
  /** The run a form acts on; the launch and clear-done forms have none. */
  paletteRunID: string | null
  paletteForwardTarget: string | null
  /** The plan the clear-done form confirms, snapshotted when it opened. */
  paletteClearDonePlan: ClearDonePlan | null
  togglePalette: (open?: boolean) => void
  openPaletteDialog: (dialog: PaletteDialog, runID?: string) => void
  openForwardDialog: (target: string) => void
  openClearDoneDialog: (plan: ClearDonePlan) => void
  closePaletteDialog: () => void
}

export const createPaletteSlice: SliceCreator<PaletteSlice> = (set) => ({
  paletteOpen: false,
  paletteDialog: null,
  paletteRunID: null,
  paletteForwardTarget: null,
  paletteClearDonePlan: null,
  togglePalette: (open) => set((s) => ({ paletteOpen: open ?? !s.paletteOpen })),
  openPaletteDialog: (dialog, runID) =>
    set({
      paletteOpen: false,
      paletteDialog: dialog,
      paletteRunID: runID ?? null,
      paletteForwardTarget: null,
      paletteClearDonePlan: null,
    }),
  openForwardDialog: (target) =>
    set({
      paletteOpen: false,
      paletteDialog: 'forward',
      paletteRunID: null,
      paletteForwardTarget: target,
      paletteClearDonePlan: null,
    }),
  openClearDoneDialog: (plan) =>
    set({
      paletteOpen: false,
      paletteDialog: 'clear-done',
      paletteRunID: null,
      paletteForwardTarget: null,
      paletteClearDonePlan: plan,
    }),
  closePaletteDialog: () =>
    set({
      paletteDialog: null,
      paletteRunID: null,
      paletteForwardTarget: null,
      paletteClearDonePlan: null,
    }),
})
