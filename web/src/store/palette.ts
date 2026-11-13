import type { Command } from '@/lib/commands'
import type { CloseOutcome, RunCloseCheck, WatchTarget } from '@/lib/types'
import { saveLaunchDraft, type LaunchDraft } from '@/components/launch/draft'
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
  /** The task the launch form opens with: text shared to Aether. */
  paletteLaunchTask: string
  launchDraft: LaunchDraft | null
  setLaunchDraft: (draft: LaunchDraft | null) => string | null
  /** A verb that closes a run with close warnings, held until the owner confirms. */
  closeConfirmation: { check: RunCloseCheck; asked: string; outcome: CloseOutcome; command: Command } | null
  /** Runs whose close check is in flight, so a second press does not check again. */
  closeChecking: Record<string, true>
  togglePalette: (open?: boolean) => void
  openPaletteDialog: (dialog: PaletteDialog, runID?: string) => void
  openLaunchWithTask: (task: string) => void
  openForwardDialog: (target: string) => void
  openWatchDialog: (target: WatchTarget) => void
  closePaletteDialog: () => void
  openCloseConfirmation: (confirmation: { check: RunCloseCheck; asked: string; outcome: CloseOutcome; command: Command }) => void
  dismissCloseConfirmation: () => void
  setCloseChecking: (runID: string, checking: boolean) => void
}

export const createPaletteSlice: SliceCreator<PaletteSlice> = (set, get) => ({
  paletteOpen: false,
  paletteDialog: null,
  paletteRunID: null,
  paletteForwardTarget: null,
  paletteWatchTarget: null,
  paletteLaunchTask: '',
  launchDraft: null,
  setLaunchDraft: (launchDraft) => {
    const error = saveLaunchDraft(get().identityKey, launchDraft)
    set({ launchDraft })
    return error
  },
  closeConfirmation: null,
  closeChecking: {},
  togglePalette: (open) => set((s) => ({ paletteOpen: open ?? !s.paletteOpen })),
  openPaletteDialog: (dialog, runID) =>
    set({
      paletteOpen: false,
      paletteDialog: dialog,
      paletteRunID: runID ?? null,
      paletteForwardTarget: null,
      paletteWatchTarget: null,
      paletteLaunchTask: '',
    }),
  openLaunchWithTask: (task) =>
    set({
      paletteOpen: false,
      paletteDialog: 'launch',
      paletteRunID: null,
      paletteForwardTarget: null,
      paletteWatchTarget: null,
      paletteLaunchTask: task,
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
      paletteLaunchTask: '',
    }),
  // A later check's answer never replaces the confirmation on screen, whose
  // Close anyway would then run another session's close.
  openCloseConfirmation: (confirmation) => set((s) => (s.closeConfirmation ? {} : { paletteOpen: false, closeConfirmation: confirmation })),
  dismissCloseConfirmation: () => set({ closeConfirmation: null }),
  setCloseChecking: (runID, checking) =>
    set((s) => {
      const closeChecking = { ...s.closeChecking }
      if (checking) closeChecking[runID] = true
      else delete closeChecking[runID]
      return { closeChecking }
    }),
})
