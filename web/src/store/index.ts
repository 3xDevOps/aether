import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import { clampTerminalFontSize } from '@/lib/term-font'
import { batched } from '@/store/batch'
import { createCollaborationSlice, type CollaborationSlice } from '@/store/collaboration'
import { createApprovalsSlice, type ApprovalsSlice } from '@/store/approvals'
import { createBoardSlice, type BoardSlice } from '@/store/board'
import { createCostSlice, type CostSlice } from '@/store/cost'
import { createDiffSlice, type DiffSlice } from '@/store/diff'
import { createFilesSlice, type FilesSlice } from '@/store/files'
import { createEnvTerminalSlice, type EnvTerminalSlice } from '@/store/env-terminal'
import { createLocalSlice, type LocalSlice } from '@/store/local'
import { createMembersSlice, type MembersSlice } from '@/store/members'
import { createPaletteSlice, type PaletteSlice } from '@/store/palette'
import { createPresenceSlice, type PresenceSlice } from '@/store/presence'
import { createRunsSlice, type RunsSlice } from '@/store/runs'
import { createServerSlice, type ServerSlice } from '@/store/server'
import { createWorkspacesSlice, type WorkspacesSlice } from '@/store/workspaces'
import { createTerminalSlice, type TerminalSlice } from '@/store/terminal'
import { createTimelineSlice, type TimelineSlice } from '@/store/timeline'
import { createMissionsSlice, type MissionsSlice } from '@/store/missions'
import {
  createUiSlice,
  normalizeBoardMapViewports,
  onboardingSteps,
  type OnboardingStep,
  type UiSlice,
} from '@/store/ui'

/**
 * Versions up to 2 persisted the onboarding resume point as an index into the
 * step list before "Git identity" was inserted at position two.
 */
const v0OnboardingSteps: OnboardingStep[] = [
  'Link',
  'Workspace',
  'Repository',
  'Agents',
  'First run',
]

export type RootState = ServerSlice &
  WorkspacesSlice &
  RunsSlice &
  MembersSlice &
  TerminalSlice &
  EnvTerminalSlice &
  BoardSlice &
  PaletteSlice &
  ApprovalsSlice &
  PresenceSlice &
  CostSlice &
  TimelineSlice &
  FilesSlice &
  DiffSlice &
  CollaborationSlice &
  LocalSlice &
  MissionsSlice &
  UiSlice

/** Only view preferences survive a reload; server data is re-hydrated. */
const persistedUi = (s: RootState) => ({
  theme: s.theme,
  sidebarWidth: s.sidebarWidth,
  sidebarCollapsed: s.sidebarCollapsed,
  terminalDockHeight: s.terminalDockHeight,
  runDockHeight: s.runDockHeight,
  terminalFontSize: s.terminalFontSize,
  singleKeyShortcuts: s.singleKeyShortcuts,
  diffWrap: s.diffWrap,
  activeWorkspace: s.activeWorkspace,
  mineOnly: s.mineOnly,
  lastHarnessByAccount: s.lastHarnessByAccount,
  boardView: s.boardView,
  boardMapViewports: s.boardMapViewports,
  dismissedUpdates: s.dismissedUpdates,
  onboarded: s.onboarded,
  onboardingStep: s.onboardingStep,
  onboardingFurthest: s.onboardingFurthest,
  onboardingWorkspace: s.onboardingWorkspace,
  onboardingSource: s.onboardingSource,
  onboardingRepo: s.onboardingRepo,
  onboardingFirstRun: s.onboardingFirstRun,
})

/** Partial: an older release stored fewer keys, and a migration may drop one. */
type PersistedState = Partial<ReturnType<typeof persistedUi>>

export function createRootStore() {
  return create<RootState>()(
    batched(persist(
      (...a) => ({
        ...createServerSlice(...a),
        ...createWorkspacesSlice(...a),
        ...createRunsSlice(...a),
        ...createMembersSlice(...a),
        ...createEnvTerminalSlice(...a),
        ...createTerminalSlice(...a),
        ...createBoardSlice(...a),
        ...createPaletteSlice(...a),
        ...createApprovalsSlice(...a),
        ...createPresenceSlice(...a),
        ...createCostSlice(...a),
        ...createTimelineSlice(...a),
        ...createDiffSlice(...a),
        ...createFilesSlice(...a),
        ...createCollaborationSlice(...a),
        ...createLocalSlice(...a),
        ...createMissionsSlice(...a),
        ...createUiSlice(...a),
      }),
      {
        name: 'aether.ui',
        version: 5,
        // Before 2 the Repository step record is unusable and is dropped, so
        // the step asks the gateway again. Before 3 the resume point is an
        // index. Before 4 there is no furthest step, so the resume point
        // stands in, or the header would turn every later step inert.
        migrate: (persisted, version): PersistedState => {
          const state: PersistedState = { ...((persisted ?? {}) as PersistedState) }
          if (version < 2) {
            delete state.onboardingRepo
          }
          if (version < 3) {
            // Not via PersistedState: that collapses the field to the current
            // name type and the compiler stops checking this conversion.
            const step = (persisted as { onboardingStep?: unknown } | null)
              ?.onboardingStep
            state.onboardingStep =
              typeof step === 'number' && v0OnboardingSteps[step]
                ? v0OnboardingSteps[step]
                : onboardingSteps[0]
          }
          if (version < 4 && state.onboardingStep) {
            state.onboardingFurthest = state.onboardingStep
          }
          if (version < 5) delete (state as { groupBy?: unknown }).groupBy
          return state
        },
        // `migrate` only runs on a version change, and xterm does not validate
        // `fontSize`, so a corrupted `terminalFontSize` is guarded here.
        merge: (persisted, current) => {
          const stored = (persisted ?? {}) as PersistedState
          return {
            ...current,
            ...stored,
            terminalFontSize: clampTerminalFontSize(
              Number(stored.terminalFontSize ?? current.terminalFontSize),
            ),
            boardView: stored.boardView === 'map' ? 'map' : 'cards',
            boardMapViewports: normalizeBoardMapViewports(stored.boardMapViewports),
          }
        },
        partialize: persistedUi,
      },
    )),
  )
}

export type RootStore = ReturnType<typeof createRootStore>

export const useStore = createRootStore()
