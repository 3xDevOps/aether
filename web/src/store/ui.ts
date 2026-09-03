import type { SliceCreator } from '@/store/slice'

export type Theme = 'light' | 'dark' | 'system'
export type GroupBy = 'status' | 'member'
/** The three things an update banner can be about. */
export type UpdateKind = 'cli' | 'server' | 'shell'

/** Where the center view is pointed. Route names come from the registry. */
export interface Route {
  name: string
  params: Record<string, string>
}

export const minSidebarWidth = 200
export const maxSidebarWidth = 520

export interface UiSlice {
  theme: Theme
  sidebarWidth: number
  sidebarCollapsed: boolean
  /**
   * The workspace every scoped surface acts on: the sidebar's run list, the
   * board, launches, templates, budgets and the activity feed. Empty until
   * hydration names one, which is why every consumer treats empty as "all".
   */
  activeWorkspace: string
  groupBy: GroupBy
  route: Route
  /**
   * Which version of each update banner the member has already dismissed,
   * keyed by kind. Holding the version rather than a boolean is the point:
   * dismissing v1.3.0 silences v1.3.0 only, and v1.3.1 shows up again.
   */
  dismissedUpdates: Record<UpdateKind, string>
  setTheme: (theme: Theme) => void
  setSidebarWidth: (width: number) => void
  toggleSidebar: () => void
  setActiveWorkspace: (workspaceID: string) => void
  setGroupBy: (groupBy: GroupBy) => void
  navigate: (name: string, params?: Record<string, string>) => void
  dismissUpdate: (kind: UpdateKind, version: string) => void
  /** Brings every dismissed banner back; the status bar's badge calls it. */
  clearDismissedUpdates: () => void
}

export const createUiSlice: SliceCreator<UiSlice> = (set, get) => ({
  theme: 'system',
  sidebarWidth: 280,
  sidebarCollapsed: false,
  activeWorkspace: '',
  groupBy: 'status',
  route: { name: 'board', params: {} },
  dismissedUpdates: { cli: '', server: '', shell: '' },
  setTheme: (theme) => set({ theme }),
  setSidebarWidth: (width) =>
    set({
      sidebarWidth: Math.min(maxSidebarWidth, Math.max(minSidebarWidth, width)),
    }),
  toggleSidebar: () => set((s) => ({ sidebarCollapsed: !s.sidebarCollapsed })),
  // Switching scope carries the workspace route with it. Otherwise the
  // switcher would say one workspace while the open view, its budget
  // dialog and its settings dialog still acted on another.
  setActiveWorkspace: (workspaceID) =>
    set((s) => ({
      activeWorkspace: workspaceID,
      route:
        s.route.name === 'workspace'
          ? { name: 'workspace', params: { workspaceId: workspaceID } }
          : s.route,
    })),
  setGroupBy: (groupBy) => set({ groupBy }),
  // Revealing a run acknowledges it, wherever the reveal came from: every
  // surface routes through this one call, so this is the only place the ack
  // belongs. Opening a workspace also makes it the active scope, so the
  // sidebar and every scoped surface follow the view.
  navigate: (name, params = {}) => {
    set({ route: { name, params } })
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
