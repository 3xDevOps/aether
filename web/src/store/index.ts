import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import { clampTerminalFontSize } from '@/lib/term-font'
import { batched } from '@/store/batch'
import { createAgentModelsSlice, type AgentModelsSlice } from '@/store/agent-models'
import { createCollaborationSlice, type CollaborationSlice } from '@/store/collaboration'
import { createApprovalsSlice, type ApprovalsSlice } from '@/store/approvals'
import { createAssistantSlice, clampAssistantWidth, onPhone, type AssistantSlice } from '@/store/assistant'
import { createBoardSlice, type BoardSlice } from '@/store/board'
import { createCostSlice, type CostSlice } from '@/store/cost'
import { createDevicesSlice, type DevicesSlice } from '@/store/devices'
import { createDiffSlice, type DiffSlice } from '@/store/diff'
import { createEdgeSlice, type EdgeSlice } from '@/store/edge'
import { createFilesSlice, type FilesSlice } from '@/store/files'
import { createEnvTerminalSlice, type EnvTerminalSlice } from '@/store/env-terminal'
import { createLocalSlice, type LocalSlice } from '@/store/local'
import { createMembersSlice, type MembersSlice } from '@/store/members'
import { createPaletteSlice, type PaletteSlice } from '@/store/palette'
import { createPresenceSlice, type PresenceSlice } from '@/store/presence'
import { createRunsSlice, type RunsSlice } from '@/store/runs'
import { createServerSlice, type ServerSlice } from '@/store/server'
import { createSessionsSlice, type SessionsSlice } from '@/store/sessions'
import { createWorkspacesSlice, type WorkspacesSlice } from '@/store/workspaces'
import { createUpdatesSlice, type UpdatesSlice } from '@/store/updates'
import { createTerminalSlice, type TerminalSlice } from '@/store/terminal'
import { createTimelineSlice, type TimelineSlice } from '@/store/timeline'
import { createMessagesSlice, type MessagesSlice } from '@/store/messages'
import { createMissionsSlice, type MissionsSlice } from '@/store/missions'
import { createViewContextSlice, type ViewContextSlice } from '@/store/view-context'
import { createWorkflowSignalsSlice, type WorkflowSignalsSlice } from '@/store/workflow-signals'
import {
  createUiSlice,
  maxSidebarWidth,
  minSidebarWidth,
  normalizeBoardMapViewports,
  normalizeBoardViews,
  onboardingSteps,
  type OnboardingStep,
  type UiSlice,
} from '@/store/ui'

/**
 * Versions up to 2 persisted the onboarding resume point as an index into the
 * step list before "Git identity" was inserted at position two.
 */
const v0OnboardingSteps = ['Link', 'Workspace', 'Repository', 'Agents', 'First run']

/** Before 8 the wizard had six steps; each maps to the step that absorbed it. */
const v7OnboardingSteps: Record<string, OnboardingStep> = {
  Link: 'Connect',
  'Git identity': 'Connect',
  Workspace: 'Repository',
  Repository: 'Repository',
  Agents: 'Agent',
  'First run': 'First run',
}

export type RootState = ServerSlice &
  AgentModelsSlice &
  WorkspacesSlice &
  RunsSlice &
  MembersSlice &
  DevicesSlice &
  EdgeSlice &
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
  UpdatesSlice &
  MissionsSlice &
  MessagesSlice &
  SessionsSlice &
  AssistantSlice &
  ViewContextSlice &
  WorkflowSignalsSlice &
  UiSlice

/** Only view preferences survive a reload; server data is re-hydrated. */
const persistedUi = (s: RootState) => ({
  theme: s.theme,
  textSize: s.textSize,
  sidebarWidth: s.sidebarWidth,
  sidebarCollapsed: s.sidebarCollapsed,
  // A phone never reopens the sheet on load.
  assistantOpen: s.assistantOpen && !onPhone(),
  assistantWidth: s.assistantWidth,
  terminalDockHeight: s.terminalDockHeight,
  detailsOpen: s.detailsOpen,
  workflowOpen: s.workflowOpen,
  browserBeside: s.browserBeside,
  browserBesideWidth: s.browserBesideWidth,
  terminalFontSize: s.terminalFontSize,
  singleKeyShortcuts: s.singleKeyShortcuts,
  diffWrap: s.diffWrap,
  activeWorkspace: s.activeWorkspace,
  mineOnly: s.mineOnly,
  boardViews: s.boardViews,
  boardMapAllWorkspaces: s.boardMapAllWorkspaces,
  boardMapViewports: s.boardMapViewports,
  launchDefaults: s.launchDefaults,
  dismissedUpdates: s.dismissedUpdates,
  sysboxNudgeDismissed: s.sysboxNudgeDismissed,
  onboarded: s.onboarded,
  onboardingStep: s.onboardingStep,
  onboardingFurthest: s.onboardingFurthest,
  onboardingWorkspace: s.onboardingWorkspace,
  onboardingSource: s.onboardingSource,
  onboardingRepo: s.onboardingRepo,
  onboardingFirstRun: s.onboardingFirstRun,
  onboardingAgentSkipped: s.onboardingAgentSkipped,
})

/** Partial: an older release stored fewer keys, and a migration may drop one. */
type PersistedState = Partial<ReturnType<typeof persistedUi>>

export function createRootStore() {
  return create<RootState>()(
    batched(persist(
      (...a) => ({
        ...createServerSlice(...a),
        ...createAgentModelsSlice(...a),
        ...createWorkspacesSlice(...a),
        ...createRunsSlice(...a),
        ...createMembersSlice(...a),
        ...createDevicesSlice(...a),
        ...createEdgeSlice(...a),
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
        ...createUpdatesSlice(...a),
        ...createMissionsSlice(...a),
        ...createMessagesSlice(...a),
        ...createSessionsSlice(...a),
        ...createAssistantSlice(...a),
        ...createViewContextSlice(...a),
        ...createWorkflowSignalsSlice(...a),
        ...createUiSlice(...a),
      }),
      {
        name: 'aether.ui',
        version: 8,
        // Before 2 the Repository step record is unusable and is dropped, so
        // the step asks the gateway again. Before 3 the resume point is an
        // index. Before 4 there is no furthest step, so the resume point
        // stands in, or the header would turn every later step inert. Before 6
        // the stored width sized the old run pane, not the sidebar. Before 8
        // the step names are the six-step wizard's and the run dock height is
        // still stored.
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
            ;(state as { onboardingStep?: string }).onboardingStep =
              typeof step === 'number' && v0OnboardingSteps[step]
                ? v0OnboardingSteps[step]
                : 'Link'
          }
          if (version < 4 && state.onboardingStep) {
            state.onboardingFurthest = state.onboardingStep
          }
          if (version < 5) delete (state as { groupBy?: unknown }).groupBy
          if (version < 6) delete state.sidebarWidth
          if (version < 6) delete (state as { lastHarnessByAccount?: unknown }).lastHarnessByAccount
          if (version < 7) {
            delete (state as { boardView?: unknown }).boardView
            delete (state as { boardMapViewports?: unknown }).boardMapViewports
          }
          if (version < 8) {
            const walk = state as { onboardingStep?: string; onboardingFurthest?: string }
            state.onboardingStep = v7OnboardingSteps[walk.onboardingStep ?? ''] ?? onboardingSteps[0]
            state.onboardingFurthest = v7OnboardingSteps[walk.onboardingFurthest ?? ''] ?? state.onboardingStep
            delete (state as { runDockHeight?: unknown }).runDockHeight
          }
          return state
        },
        // `migrate` only runs on a version change, and xterm does not validate
        // `fontSize`, so a corrupted `terminalFontSize` is guarded here.
        merge: (persisted, current) => {
          const stored = (persisted ?? {}) as PersistedState
          return {
            ...current,
            ...stored,
            boardViews: normalizeBoardViews(stored.boardViews),
            boardMapAllWorkspaces: stored.boardMapAllWorkspaces === true,
            boardMapViewports: normalizeBoardMapViewports(stored.boardMapViewports),
            sidebarWidth: Math.min(maxSidebarWidth, Math.max(minSidebarWidth, Number(stored.sidebarWidth) || current.sidebarWidth)),
            assistantOpen: stored.assistantOpen === true && !onPhone(),
            assistantWidth: clampAssistantWidth(Number(stored.assistantWidth) || current.assistantWidth),
            terminalFontSize: clampTerminalFontSize(
              Number(stored.terminalFontSize ?? current.terminalFontSize),
            ),
          }
        },
        partialize: persistedUi,
      },
    )),
  )
}

export type RootStore = ReturnType<typeof createRootStore>

export const useStore = createRootStore()

/** Whether the signed-in identity is still the one at this call. A sign-in change clears the old member's state, so an answer for them must not land in the new member's. */
export function sameIdentity(): () => boolean {
  const { identityKey } = useStore.getState()
  return () => useStore.getState().identityKey === identityKey
}
