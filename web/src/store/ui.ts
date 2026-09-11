import { clampDockHeight } from '@/components/dock'
import { clampTerminalFontSize, defaultTerminalFontSize } from '@/lib/term-font'
import type {
  LinkRepoResult,
  RepoFastForwardResult,
  RepoPushResult,
} from '@/lib/types'
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
 * remote the gateway wrote, git's answer to the seeding push once one has
 * run, and the fast-forward that answered a workspace ahead of the clone.
 * It outlives the step so walking back into Repository shows the settled
 * answer rather than an empty form or a button that was already pressed.
 * The workspace it was settled for is part of it, because a remote points at
 * one workspace: picking a different one leaves this stale, and the step
 * must ask again.
 */
export interface OnboardingRepo {
  /**
   * Identifies this connection, not the clone it points at. Re-pointing
   * and reconnecting the same path to the same workspace is a different
   * connection, and a push still in flight from the previous one has to be
   * told apart from it; path and workspace alone cannot do that.
   */
  link: string
  workspace: string
  path: string
  remote: LinkRepoResult
  push: RepoPushResult | null
  fastForward: RepoFastForwardResult | null
}

/**
 * The onboarding wizard's steps, in order. The resume point is persisted as
 * one of these names rather than as a position, so inserting a step never
 * relocates someone who is mid-wizard.
 */
export const onboardingSteps = [
  'Link',
  'Git identity',
  'Workspace',
  'Repository',
  'Agents',
  'First run',
] as const

export type OnboardingStep = (typeof onboardingSteps)[number]

/** The First run step's unlaunched draft. */
export interface OnboardingFirstRun {
  harness: string
  task: string
}

const emptyFirstRun: OnboardingFirstRun = { harness: '', task: '' }

/** What leaving the wizard clears: the walk, not the member's preferences. */
const wizardReset = {
  onboardingStep: 'Link',
  onboardingFurthest: 'Link',
  onboardingWorkspace: '',
  onboardingRepo: null,
  onboardingFirstRun: emptyFirstRun,
} as const

/** Where to resume; anything the wizard no longer knows starts over. */
export function onboardingStepIndex(step: OnboardingStep): number {
  return Math.max(0, onboardingSteps.indexOf(step))
}

export const minSidebarWidth = 200
export const maxSidebarWidth = 520

export interface UiSlice {
  theme: Theme
  sidebarWidth: number
  sidebarCollapsed: boolean
  terminalDockHeight: number
  runDockHeight: number
  /** Zoom level shared by every terminal, in pixels. */
  terminalFontSize: number
  /**
   * Whether the Diff tab wraps long lines, or null while it still follows
   * the pointer. One run-detail route is mounted at a time, so component
   * state would reset the toggle every time the member left Diff and came
   * back - several times a minute on a phone.
   */
  diffWrap: boolean | null
  /**
   * Whether the member has ever taken control of a run. Until they have, the
   * Terminal tab says what the default attach is, because nothing else on
   * screen distinguishes a read-only mirror from a steered session.
   */
  terminalControlTaken: boolean
  onboarded: boolean
  onboardingStep: OnboardingStep
  /**
   * The furthest step reached, which never falls back on its own. The header
   * marks these done and lets the member jump between them: without it a
   * jump backwards would make every later step unreachable, and Link has no
   * Back of its own to escape with.
   */
  onboardingFurthest: OnboardingStep
  onboardingWorkspace: string
  onboardingRepo: OnboardingRepo | null
  /** What the First run step has typed but not launched. It lives here so a
   * jump to another step and back does not throw the draft away. */
  onboardingFirstRun: OnboardingFirstRun
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
  setTerminalFontSize: (size: number) => void
  setDiffWrap: (wrap: boolean) => void
  markTerminalControlTaken: () => void
  toggleSidebar: () => void
  setOnboarded: (onboarded: boolean) => void
  setOnboardingStep: (step: OnboardingStep) => void
  setOnboardingWorkspace: (workspaceID: string) => void
  setOnboardingRepo: (repo: OnboardingRepo | null) => void
  setOnboardingFirstRun: (draft: OnboardingFirstRun) => void
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
  terminalFontSize: defaultTerminalFontSize,
  diffWrap: null,
  terminalControlTaken: false,
  onboarded: false,
  onboardingStep: 'Link',
  onboardingFurthest: 'Link',
  onboardingWorkspace: '',
  onboardingRepo: null,
  onboardingFirstRun: emptyFirstRun,
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
  setTerminalFontSize: (size) => set({ terminalFontSize: clampTerminalFontSize(size) }),
  setDiffWrap: (diffWrap) => set({ diffWrap }),
  markTerminalControlTaken: () => set({ terminalControlTaken: true }),
  toggleSidebar: () => set((s) => ({ sidebarCollapsed: !s.sidebarCollapsed })),
  setOnboarded: (onboarded) =>
    set(
      onboarded ? { onboarded: true, ...wizardReset } : { onboarded: false },
    ),
  setOnboardingStep: (onboardingStep) =>
    set((s) => ({
      onboardingStep,
      onboardingFurthest:
        onboardingStepIndex(onboardingStep) > onboardingStepIndex(s.onboardingFurthest)
          ? onboardingStep
          : s.onboardingFurthest,
    })),
  setOnboardingWorkspace: (onboardingWorkspace) => set({ onboardingWorkspace }),
  setOnboardingRepo: (onboardingRepo) => set({ onboardingRepo }),
  setOnboardingFirstRun: (onboardingFirstRun) => set({ onboardingFirstRun }),
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
        ? { onboarded: true, ...wizardReset }
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
