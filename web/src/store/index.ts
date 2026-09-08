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
import { createUiSlice, type UiSlice } from '@/store/ui'

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

/**
 * What survives a reload. Reading it back yields a partial: an older release
 * stored fewer keys, and a migration may drop one.
 */
type PersistedState = Partial<
  Pick<
    RootState,
    | 'theme'
    | 'sidebarWidth'
    | 'sidebarCollapsed'
    | 'terminalDockHeight'
    | 'runDockHeight'
    | 'activeWorkspace'
    | 'groupBy'
    | 'lastHarnessByAccount'
    | 'dismissedUpdates'
    | 'onboarded'
    | 'onboardingStep'
    | 'onboardingWorkspace'
    | 'onboardingRepo'
  >
>

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
        version: 1,
        // Version 0 stored the Repository step's push answer before it
        // carried the comparison state the step renders, so it comes back
        // matching no state at all. Dropping it puts the step back on its
        // push offer, which asks the gateway again.
        migrate: (persisted): PersistedState => {
          const { onboardingRepo, ...rest } = (persisted ??
            {}) as PersistedState
          return rest
        },
        // Only view preferences survive a reload; server data is re-hydrated.
        partialize: (s): PersistedState => ({
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
        }),
      },
    ),
  )
}

export type RootStore = ReturnType<typeof createRootStore>

export const useStore = createRootStore()
