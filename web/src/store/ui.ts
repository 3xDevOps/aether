import { clampDockHeight } from '@/components/dock'
import { clampTerminalFontSize, defaultTerminalFontSize } from '@/lib/term-font'
import { initialRoute } from '@/lib/url-state'
import { isRunView, type RunView } from '@/routes/run/views'
import type {
  ConfigExclusion,
  ConfigImportResult,
  LaunchMode,
  LinkRepoResult,
  RepoFastForwardResult,
  RepoPushResult,
} from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

export type Theme = 'light' | 'dark' | 'system'
export type TextSize = 'default' | 'large' | 'larger'
export type UpdateKind = 'cli' | 'server' | 'shell'
export type BoardView = 'board' | 'map'

export interface BoardMapViewport {
  x: number
  y: number
  zoom: number
}

export const minBoardMapZoom = 0.02
export const maxBoardMapZoom = 2

export function normalizeBoardMapViewport(value: unknown): BoardMapViewport | null {
  if (!value || typeof value !== 'object') return null
  const { x, y, zoom } = value as BoardMapViewport
  if (!Number.isFinite(x) || !Number.isFinite(y) || !Number.isFinite(zoom) || zoom <= 0) return null
  return {
    x: Math.max(-10_000_000, Math.min(10_000_000, x)),
    y: Math.max(-10_000_000, Math.min(10_000_000, y)),
    zoom: Math.max(minBoardMapZoom, Math.min(maxBoardMapZoom, zoom)),
  }
}

export function normalizeBoardMapViewports(value: unknown): Record<string, BoardMapViewport> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {}
  const viewports: [string, BoardMapViewport][] = []
  for (const [scope, candidate] of Object.entries(value)) {
    const viewport = normalizeBoardMapViewport(candidate)
    if (viewport) viewports.push([scope, viewport])
  }
  return Object.fromEntries(viewports)
}

export function normalizeBoardViews(value: unknown): Record<string, BoardView> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {}
  return Object.fromEntries(Object.entries(value).filter((entry) => entry[1] === 'board' || entry[1] === 'map'))
}

export interface Route {
  name: string
  params: Record<string, string>
}

/** Outlives the step so walking back shows the settled answer. A remote
 * points at one workspace, so picking another one leaves this stale. */
export interface OnboardingRepo {
  /** Identifies this connection, not the clone: a push still in flight from
   * an earlier connection of the same path and workspace must be told apart. */
  link: string
  workspace: string
  path: string
  remote: LinkRepoResult
  push: RepoPushResult | null
  fastForward: RepoFastForwardResult | null
}

/** The resume point is persisted by name, not position, so inserting a step
 * never relocates someone mid-wizard. */
export const onboardingSteps = ['Connect', 'Repository', 'Agent', 'First run'] as const

export type OnboardingStep = (typeof onboardingSteps)[number]

export interface OnboardingFirstRun {
  harness: string
  task: string
}

const emptyFirstRun: OnboardingFirstRun = { harness: '', task: '' }

/** What leaving the wizard clears: the walk, not the member's preferences. */
const wizardReset = {
  onboardingStep: 'Connect',
  onboardingFurthest: 'Connect',
  onboardingWorkspace: '',
  onboardingSource: 'remote',
  onboardingRepo: null,
  onboardingFirstRun: emptyFirstRun,
  onboardingAgentSkipped: false,
} as const

/** Where to resume; anything the wizard no longer knows starts over. */
export function onboardingStepIndex(step: OnboardingStep): number {
  return Math.max(0, onboardingSteps.indexOf(step))
}

export const minSidebarWidth = 220
export const maxSidebarWidth = 400
export const defaultSidebarWidth = 260

export interface ConfigImportCandidate {
  path: string
  destinationPath: string
  file: File
  problem?: ConfigExclusion
}

export interface ConfigImportStatus {
  owner: string | null
  basename: string
  destination: string
  totalFiles: number
  excluded: ConfigExclusion[]
  phase: 'reading' | 'uploading' | 'complete'
  result: ConfigImportResult
  unknownPaths: string[]
  remaining: ConfigImportCandidate[]
  errors: string[]
}

export interface UiSlice {
  theme: Theme
  textSize: TextSize
  sidebarWidth: number
  sidebarCollapsed: boolean
  sidebarDrawerOpen: boolean
  /** Mounted pages showing their own filled action in their header. */
  headerPrimaries: number
  terminalDockHeight: number
  /** Zoom level shared by every terminal, in pixels. */
  terminalFontSize: number
  /** Off makes character shortcuts (`n`, `?`, `g b`) inert, for speech
   * input that types words the dashboard would read as commands. */
  singleKeyShortcuts: boolean
  /** Null while it follows the pointer. Stored here because component state
   * would reset whenever the member leaves Diff and comes back. */
  diffWrap: boolean | null
  onboarded: boolean
  onboardingStep: OnboardingStep
  /** Never falls back on its own: a backwards jump would otherwise make later
   * steps unreachable, and Link has no Back to escape with. */
  onboardingFurthest: OnboardingStep
  onboardingWorkspace: string
  onboardingSource: 'local' | 'remote'
  onboardingRepo: OnboardingRepo | null
  configImportPending: boolean
  configImportStatus: ConfigImportStatus | null
  /** Here so a jump to another step and back keeps the draft. */
  onboardingFirstRun: OnboardingFirstRun
  onboardingAgentSkipped: boolean
  /** Empty until hydration names one; every consumer treats empty as "all". */
  activeWorkspace: string
  mineOnly: boolean
  boardViews: Record<string, BoardView>
  boardMapAllWorkspaces: boolean
  boardMapViewports: Record<string, BoardMapViewport>
  /** Keyed by agent name; the newest `at` is the agent a launch preselects. */
  launchDefaults: Record<string, { mode: LaunchMode; at: number }>
  route: Route
  /** The view each run was last shown in during this visit; never persisted. */
  runViewMemory: Record<string, RunView>
  detailsOpen: boolean
  /** Whether a run's Browser sits beside its other views on a wide window. */
  browserBeside: boolean
  /** Null until the divider is moved: half the run's frame. */
  browserBesideWidth: number | null
  /** The viewport preset each run's Browser was given; without one it follows
   * its pane. Never persisted. */
  browserPresets: Record<string, string>
  /** The address each run's Browser was asked to open and has not taken yet;
   * never persisted. */
  browserRequests: Record<string, string>
  /** A version, not a boolean: dismissing v1.3.0 still shows v1.3.1. */
  dismissedUpdates: Record<UpdateKind, string>
  updatesOpen: boolean
  shortcutsOpen: boolean
  setTheme: (theme: Theme) => void
  setTextSize: (size: TextSize) => void
  setSidebarWidth: (width: number) => void
  setSidebarDrawerOpen: (open: boolean) => void
  holdHeaderPrimary: () => () => void
  setTerminalDockHeight: (height: number) => void
  setTerminalFontSize: (size: number) => void
  setSingleKeyShortcuts: (on: boolean) => void
  setDiffWrap: (wrap: boolean) => void
  toggleSidebar: () => void
  setOnboarded: (onboarded: boolean) => void
  setOnboardingStep: (step: OnboardingStep) => void
  setOnboardingWorkspace: (workspaceID: string) => void
  setOnboardingSource: (source: 'local' | 'remote') => void
  setOnboardingRepo: (repo: OnboardingRepo | null) => void
  setOnboardingFirstRun: (draft: OnboardingFirstRun) => void
  setOnboardingAgentSkipped: (skipped: boolean) => void
  setActiveWorkspace: (workspaceID: string) => void
  setMineOnly: (mineOnly: boolean) => void
  setBoardView: (scope: string, view: BoardView) => void
  setBoardMapAllWorkspaces: (all: boolean) => void
  setBoardMapViewport: (scope: string, viewport: BoardMapViewport) => void
  rememberLaunch: (agent: string, mode: LaunchMode) => void
  setLaunchDefault: (agent: string, mode: LaunchMode) => void
  navigate: (name: string, params?: Record<string, string>) => void
  dismissUpdate: (kind: UpdateKind, version: string) => void
  clearDismissedUpdates: () => void
  setUpdatesOpen: (open: boolean) => void
  setShortcutsOpen: (open: boolean) => void
  setDetailsOpen: (open: boolean) => void
  setBrowserBeside: (beside: boolean) => void
  setBrowserBesideWidth: (width: number) => void
  setBrowserPreset: (runID: string, preset: string | null) => void
  requestBrowser: (runID: string, address: string | null) => void
}

function withEntry(record: Record<string, string>, key: string, value: string | null): Record<string, string> {
  const next = { ...record }
  if (value === null) delete next[key]
  else next[key] = value
  return next
}

export const createUiSlice: SliceCreator<UiSlice> = (set) => ({
  theme: 'system',
  textSize: 'default',
  sidebarWidth: defaultSidebarWidth,
  sidebarCollapsed: false,
  sidebarDrawerOpen: false,
  headerPrimaries: 0,
  terminalDockHeight: 280,
  terminalFontSize: defaultTerminalFontSize,
  singleKeyShortcuts: true,
  diffWrap: null,
  onboarded: false,
  onboardingStep: 'Connect',
  onboardingFurthest: 'Connect',
  onboardingWorkspace: '',
  onboardingSource: 'remote',
  onboardingRepo: null,
  configImportPending: false,
  configImportStatus: null,
  onboardingFirstRun: emptyFirstRun,
  onboardingAgentSkipped: false,
  activeWorkspace: '',
  mineOnly: false,
  boardViews: {},
  boardMapAllWorkspaces: false,
  boardMapViewports: {},
  launchDefaults: {},
  route: initialRoute(),
  runViewMemory: {},
  detailsOpen: true,
  browserBeside: false,
  browserBesideWidth: null,
  browserPresets: {},
  browserRequests: {},
  dismissedUpdates: { cli: '', server: '', shell: '' },
  updatesOpen: false,
  shortcutsOpen: false,
  setTheme: (theme) => set({ theme }),
  setTextSize: (textSize) => set({ textSize }),
  setSidebarWidth: (width) =>
    set({
      sidebarWidth: Math.min(maxSidebarWidth, Math.max(minSidebarWidth, width)),
    }),
  setTerminalDockHeight: (height) => set({ terminalDockHeight: clampDockHeight(height) }),
  setTerminalFontSize: (size) => set({ terminalFontSize: clampTerminalFontSize(size) }),
  setSingleKeyShortcuts: (singleKeyShortcuts) => set({ singleKeyShortcuts }),
  setDiffWrap: (diffWrap) => set({ diffWrap }),
  toggleSidebar: () => set((s) => ({ sidebarCollapsed: !s.sidebarCollapsed })),
  setSidebarDrawerOpen: (sidebarDrawerOpen) => set({ sidebarDrawerOpen }),
  holdHeaderPrimary: () => {
    set((s) => ({ headerPrimaries: s.headerPrimaries + 1 }))
    return () => set((s) => ({ headerPrimaries: s.headerPrimaries - 1 }))
  },
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
  setOnboardingSource: (onboardingSource) => set({ onboardingSource }),
  setOnboardingRepo: (onboardingRepo) => set({ onboardingRepo }),
  setOnboardingFirstRun: (onboardingFirstRun) => set({ onboardingFirstRun }),
  setOnboardingAgentSkipped: (onboardingAgentSkipped) => set({ onboardingAgentSkipped }),
  // Carry the workspace route along, or the open view would act on a
  // different workspace than the switcher shows.
  setActiveWorkspace: (workspaceID) =>
    set((s) => ({
      activeWorkspace: workspaceID,
      route:
        s.route.name === 'workspace'
          ? { name: 'workspace', params: { workspaceId: workspaceID } }
          : s.route,
    })),
  setMineOnly: (mineOnly) => set({ mineOnly }),
  setBoardView: (scope, view) => set((s) => ({ boardViews: { ...s.boardViews, [scope]: view } })),
  setBoardMapAllWorkspaces: (boardMapAllWorkspaces) => set({ boardMapAllWorkspaces }),
  setBoardMapViewport: (scope, value) => {
    const viewport = normalizeBoardMapViewport(value)
    if (viewport) set((s) => ({ boardMapViewports: { ...s.boardMapViewports, [scope]: viewport } }))
  },
  rememberLaunch: (agent, mode) =>
    set((s) => ({ launchDefaults: { ...s.launchDefaults, [agent]: { mode, at: Date.now() } } })),
  // Keeps `at`, which ranks the agent the launch dialog preselects.
  setLaunchDefault: (agent, mode) =>
    set((s) => ({ launchDefaults: { ...s.launchDefaults, [agent]: { mode, at: s.launchDefaults[agent]?.at ?? 0 } } })),
  navigate: (name, params = {}) => {
    set((s) => ({
      route: { name, params },
      ...(name === 'run' && params.runId && isRunView(params.view)
        ? { runViewMemory: { ...s.runViewMemory, [params.runId]: params.view } }
        : {}),
      ...(s.route.name === 'onboarding' && name !== 'onboarding'
        ? { onboarded: true, onboardingStep: s.onboardingFurthest }
        : {}),
    }))
    if (name === 'workspace' && params.workspaceId) {
      set({ activeWorkspace: params.workspaceId })
    }
  },
  dismissUpdate: (kind, version) =>
    set((s) => ({ dismissedUpdates: { ...s.dismissedUpdates, [kind]: version } })),
  clearDismissedUpdates: () =>
    set({ dismissedUpdates: { cli: '', server: '', shell: '' } }),
  setUpdatesOpen: (updatesOpen) => set({ updatesOpen }),
  setShortcutsOpen: (shortcutsOpen) => set({ shortcutsOpen }),
  setDetailsOpen: (detailsOpen) => set({ detailsOpen }),
  setBrowserBeside: (browserBeside) => set({ browserBeside }),
  setBrowserBesideWidth: (browserBesideWidth) => set({ browserBesideWidth }),
  setBrowserPreset: (runID, preset) => set((s) => ({ browserPresets: withEntry(s.browserPresets, runID, preset) })),
  requestBrowser: (runID, address) => set((s) => ({ browserRequests: withEntry(s.browserRequests, runID, address) })),
})
