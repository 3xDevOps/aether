import { create } from 'zustand'
import { persist } from 'zustand/middleware'
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
import {
  createUiSlice,
  onboardingSteps,
  type OnboardingStep,
  type UiSlice,
} from '@/store/ui'

/**
 * Every version up to 2 persisted the onboarding resume point as an index
 * into the step list as it stood before "Git identity" was inserted at
 * position two. The names are what those stored numbers meant; typing them
 * as OnboardingStep keeps this list from drifting away from the wizard's
 * own.
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
  LocalSlice &
  UiSlice

/** Only view preferences survive a reload; server data is re-hydrated. */
const persistedUi = (s: RootState) => ({
  theme: s.theme,
  sidebarWidth: s.sidebarWidth,
  sidebarCollapsed: s.sidebarCollapsed,
  terminalDockHeight: s.terminalDockHeight,
  runDockHeight: s.runDockHeight,
  activeWorkspace: s.activeWorkspace,
  groupBy: s.groupBy,
  lastHarnessByAccount: s.lastHarnessByAccount,
  dismissedUpdates: s.dismissedUpdates,
  onboarded: s.onboarded,
  onboardingStep: s.onboardingStep,
  onboardingWorkspace: s.onboardingWorkspace,
  onboardingRepo: s.onboardingRepo,
})

/**
 * What survives a reload. Reading it back yields a partial: an older release
 * stored fewer keys, and a migration may drop one.
 */
type PersistedState = Partial<ReturnType<typeof persistedUi>>

/**
 * The root store: one Zustand store composed of independent slices. A new
 * feature adds a slice file and one line here.
 */
export function createRootStore() {
  return create<RootState>()(
    persist(
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
        ...createLocalSlice(...a),
        ...createUiSlice(...a),
      }),
      {
        name: 'aether.ui',
        version: 3,
        // Every version before 2 stored a Repository step record this build
        // cannot use: version 0's push answer predates the comparison state
        // the step renders, and version 1 has no link id to tell one
        // connection from the next. Dropping it puts the step back on its
        // push offer, which asks the gateway again. Every version before 3
        // stored the resume point as an index, so the number is read back
        // as the step it meant.
        migrate: (persisted, version): PersistedState => {
          const stored = (persisted ?? {}) as PersistedState & {
            onboardingStep?: unknown
          }
          const state: PersistedState = { ...stored }
          if (version < 2) {
            delete state.onboardingRepo
          }
          if (version < 3) {
            const index = stored.onboardingStep
            state.onboardingStep =
              typeof index === 'number' && v0OnboardingSteps[index]
                ? v0OnboardingSteps[index]
                : onboardingSteps[0]
          }
          return state
        },
        // Only view preferences survive a reload; server data is re-hydrated.
        partialize: persistedUi,
      },
    ),
  )
}

export type RootStore = ReturnType<typeof createRootStore>

export const useStore = createRootStore()
