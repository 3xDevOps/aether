import { clampDockHeight } from '@/components/dock'
import type { LinkRepoResult, RepoPushResult } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

export type Theme = 'light' | 'dark' | 'system'
export type GroupBy = 'status' | 'member'
/** The three things an update banner can be about. */
export type UpdateKind = 'cli' | 'server' | 'shell'

/** Where the center view is pointed. */
export interface Route {
  name: string
  params: Record<string, string>
}

/**
 * What the onboarding Repository step settled: the clone it pointed at, the
 * remote the gateway wrote, and git's answer to the seeding push once one
 * has run. It outlives the step so walking back into Repository shows the
 * connected repo rather than an empty form. The workspace it was settled
 * for is part of it, because a remote points at one workspace: picking a
 * different one leaves this stale, and the step must ask again.
 */
export interface OnboardingRepo {
  workspace: string
  path: string
  remote: LinkRepoResult
  push: RepoPushResult | null
}

export const minSidebarWidth = 200
export const maxSidebarWidth = 520

export interface UiSlice {
  theme: Theme
  sidebarWidth: number
  sidebarCollapsed: boolean
  terminalDockHeight: number
  runDockHeight: number
  onboarded: boolean
  onboardingStep: number
  onboardingWorkspace: string
  onboardingRepo: OnboardingRepo | null
  /**
   * The workspace every scoped surface acts on: the sidebar's run list, the
   * board, launches, templates, budgets and the activity feed. Empty until
   * hydration names one, which is why every consumer treats empty as "all".
   */
  activeWorkspace: string
  groupBy: GroupBy
  /**
   * The last harness successfully used for each agent account. This is a
   * preference, not run state: it survives run cleanup and gives a launch
   * form with no run history the same default the member chose last time.
   */
  lastHarnessByAccount: Record<string, string>
  route: Route
  /**
   * Which version of each update banner the member has already dismissed,
   * keyed by kind. Holding the version rather than a boolean is the point:
   * dismissing v1.3.0 silences v1.3.0 only, and v1.3.1 shows up again.
   */
  dismissedUpdates: Record<UpdateKind, string>
  setTheme: (theme: Theme) => void
  setSidebarWidth: (width: number) => void
  setTerminalDockHeight: (height: number) => void
  setRunDockHeight: (height: number) => void
  toggleSidebar: () => void
  setOnboarded: (onboarded: boolean) => void
  setOnboardingStep: (step: number) => void
  setOnboardingWorkspace: (workspaceID: string) => void
  setOnboardingRepo: (repo: OnboardingRepo | null) => void
  setActiveWorkspace: (workspaceID: string) => void
  setGroupBy: (groupBy: GroupBy) => void
  rememberHarness: (accountID: string, harness: string) => void
  navigate: (name: string, params?: Record<string, string>) => void
  dismissUpdate: (kind: UpdateKind, version: string) => void
  /** Brings every dismissed banner back; the status bar's badge calls it. */
  clearDismissedUpdates: () => void
}

export const createUiSlice: SliceCreator<UiSlice> = (set, get) => ({
  theme: 'system',
  sidebarWidth: 280,
  sidebarCollapsed: false,
  terminalDockHeight: 280,
  runDockHeight: 240,
  onboarded: false,
  onboardingStep: 0,
  onboardingWorkspace: '',
  onboardingRepo: null,
  activeWorkspace: '',
  groupBy: 'status',
  lastHarnessByAccount: {},
  route: { name: 'board', params: {} },
  dismissedUpdates: { cli: '', server: '', shell: '' },
  setTheme: (theme) => set({ theme }),
  setSidebarWidth: (width) =>
    set({
      sidebarWidth: Math.min(maxSidebarWidth, Math.max(minSidebarWidth, width)),
    }),
  setTerminalDockHeight: (height) => set({ terminalDockHeight: clampDockHeight(height) }),
  setRunDockHeight: (height) => set({ runDockHeight: clampDockHeight(height) }),
  toggleSidebar: () => set((s) => ({ sidebarCollapsed: !s.sidebarCollapsed })),
  setOnboarded: (onboarded) =>
    set(
      onboarded
        ? {
            onboarded: true,
            onboardingStep: 0,
            onboardingWorkspace: '',
            onboardingRepo: null,
          }
        : { onboarded: false },
    ),
  setOnboardingStep: (onboardingStep) => set({ onboardingStep }),
  setOnboardingWorkspace: (onboardingWorkspace) => set({ onboardingWorkspace }),
  setOnboardingRepo: (onboardingRepo) => set({ onboardingRepo }),
  // Switching scope carries the workspace route with it. Otherwise the
  // switcher would say one workspace while the open view, its budget dialog
  // and its settings dialog still acted on another.
  setActiveWorkspace: (workspaceID) =>
    set((s) => ({
      activeWorkspace: workspaceID,
      route:
        s.route.name === 'workspace'
          ? { name: 'workspace', params: { workspaceId: workspaceID } }
          : s.route,
    })),
  setGroupBy: (groupBy) => set({ groupBy }),
  rememberHarness: (accountID, harness) => {
    if (!accountID || !harness) return
    set((s) => ({
      lastHarnessByAccount: { ...s.lastHarnessByAccount, [accountID]: harness },
    }))
  },
  // Revealing a run acknowledges it, wherever the reveal came from: every
  // surface routes through this one call, so this is the only place the ack
  // belongs. Opening a workspace also makes it the active scope, so the
  // sidebar and every scoped surface follow the view.
  navigate: (name, params = {}) => {
    set((s) => ({
      route: { name, params },
      ...(s.route.name === 'onboarding' && name !== 'onboarding'
        ? {
            onboarded: true,
            onboardingStep: 0,
            onboardingWorkspace: '',
            onboardingRepo: null,
          }
        : {}),
    }))
    if (params.runId) get().ackRun(params.runId)
    if (name === 'workspace' && params.workspaceId) {
      set({ activeWorkspace: params.workspaceId })
    }
  },
  dismissUpdate: (kind, version) =>
    set((s) => ({ dismissedUpdates: { ...s.dismissedUpdates, [kind]: version } })),
  clearDismissedUpdates: () =>
    set({ dismissedUpdates: { cli: '', server: '', shell: '' } }),
})
