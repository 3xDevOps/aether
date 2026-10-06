import { clampDockHeight } from '@/components/dock'
import { clampTerminalFontSize, defaultTerminalFontSize } from '@/lib/term-font'
import { initialRoute } from '@/lib/url-state'
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
export type BoardView = 'cards' | 'map'

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
  if (
    !Number.isFinite(x) ||
    !Number.isFinite(y) ||
    !Number.isFinite(zoom) ||
    zoom <= 0
  ) return null
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

export type UpdateKind = 'cli' | 'server' | 'shell'

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
export const onboardingSteps = [
  'Link',
  'Git identity',
  'Workspace',
  'Repository',
  'Agents',
  'First run',
] as const

export type OnboardingStep = (typeof onboardingSteps)[number]

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
  onboardingSource: 'remote',
  onboardingRepo: null,
  onboardingFirstRun: emptyFirstRun,
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
  sidebarWidth: number
  sidebarCollapsed: boolean
  sidebarDrawerOpen: boolean
  terminalDockHeight: number
  runDockHeight: number
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
  /** Empty until hydration names one; every consumer treats empty as "all". */
  activeWorkspace: string
  mineOnly: boolean
  boardView: BoardView
  boardMapViewports: Record<string, BoardMapViewport>
  /** Keyed by agent name; the newest `at` is the agent a launch preselects. */
  launchDefaults: Record<string, { mode: LaunchMode; at: number }>
  route: Route
  /** A version, not a boolean: dismissing v1.3.0 still shows v1.3.1. */
  dismissedUpdates: Record<UpdateKind, string>
  updatesOpen: boolean
  shortcutsOpen: boolean
  setTheme: (theme: Theme) => void
  setSidebarWidth: (width: number) => void
  setSidebarDrawerOpen: (open: boolean) => void
  setTerminalDockHeight: (height: number) => void
  setRunDockHeight: (height: number) => void
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
  setActiveWorkspace: (workspaceID: string) => void
  setMineOnly: (mineOnly: boolean) => void
  setBoardView: (view: BoardView) => void
  setBoardMapViewport: (scope: string, viewport: BoardMapViewport) => void
  rememberLaunch: (agent: string, mode: LaunchMode) => void
  navigate: (name: string, params?: Record<string, string>) => void
  dismissUpdate: (kind: UpdateKind, version: string) => void
  clearDismissedUpdates: () => void
  setUpdatesOpen: (open: boolean) => void
  setShortcutsOpen: (open: boolean) => void
}

export const createUiSlice: SliceCreator<UiSlice> = (set) => ({
  theme: 'system',
  sidebarWidth: defaultSidebarWidth,
  sidebarCollapsed: false,
  sidebarDrawerOpen: false,
  terminalDockHeight: 280,
  runDockHeight: 240,
  terminalFontSize: defaultTerminalFontSize,
  singleKeyShortcuts: true,
  diffWrap: null,
  onboarded: false,
  onboardingStep: 'Link',
  onboardingFurthest: 'Link',
  onboardingWorkspace: '',
  onboardingSource: 'remote',
  onboardingRepo: null,
  configImportPending: false,
  configImportStatus: null,
  onboardingFirstRun: emptyFirstRun,
  activeWorkspace: '',
  mineOnly: false,
  boardView: 'cards',
  boardMapViewports: {},
  launchDefaults: {},
  route: initialRoute(),
  dismissedUpdates: { cli: '', server: '', shell: '' },
  updatesOpen: false,
  shortcutsOpen: false,
  setTheme: (theme) => set({ theme }),
  setSidebarWidth: (width) =>
    set({
      sidebarWidth: Math.min(maxSidebarWidth, Math.max(minSidebarWidth, width)),
    }),
  setTerminalDockHeight: (height) => set({ terminalDockHeight: clampDockHeight(height) }),
  setRunDockHeight: (height) => set({ runDockHeight: clampDockHeight(height) }),
  setTerminalFontSize: (size) => set({ terminalFontSize: clampTerminalFontSize(size) }),
  setSingleKeyShortcuts: (singleKeyShortcuts) => set({ singleKeyShortcuts }),
  setDiffWrap: (diffWrap) => set({ diffWrap }),
  toggleSidebar: () => set((s) => ({ sidebarCollapsed: !s.sidebarCollapsed })),
  setSidebarDrawerOpen: (sidebarDrawerOpen) => set({ sidebarDrawerOpen }),
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
  setBoardView: (boardView) => set({ boardView }),
  setBoardMapViewport: (scope, value) => {
    const viewport = normalizeBoardMapViewport(value)
    if (!viewport) return
    set((s) => ({
      boardMapViewports: { ...s.boardMapViewports, [scope]: viewport },
    }))
  },
  rememberLaunch: (agent, mode) =>
    set((s) => ({ launchDefaults: { ...s.launchDefaults, [agent]: { mode, at: Date.now() } } })),
  navigate: (name, params = {}) => {
    set((s) => ({
      route: { name, params },
      ...(s.route.name === 'onboarding' && name !== 'onboarding'
        ? { onboarded: true, ...wizardReset }
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
})
