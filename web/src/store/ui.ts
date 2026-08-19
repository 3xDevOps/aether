import type { SliceCreator } from '@/store/slice'

export type Theme = 'light' | 'dark' | 'system'
export type GroupBy = 'status' | 'member'

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
  collapsedSessions: string[]
  groupBy: GroupBy
  route: Route
  setTheme: (theme: Theme) => void
  setSidebarWidth: (width: number) => void
  toggleSidebar: () => void
  toggleSession: (sessionID: string) => void
  setGroupBy: (groupBy: GroupBy) => void
  navigate: (name: string, params?: Record<string, string>) => void
}

export const createUiSlice: SliceCreator<UiSlice> = (set, get) => ({
  theme: 'system',
  sidebarWidth: 280,
  sidebarCollapsed: false,
  collapsedSessions: [],
  groupBy: 'status',
  route: { name: 'board', params: {} },
  setTheme: (theme) => set({ theme }),
  setSidebarWidth: (width) =>
    set({
      sidebarWidth: Math.min(maxSidebarWidth, Math.max(minSidebarWidth, width)),
    }),
  toggleSidebar: () => set((s) => ({ sidebarCollapsed: !s.sidebarCollapsed })),
  toggleSession: (sessionID) =>
    set((s) => ({
      collapsedSessions: s.collapsedSessions.includes(sessionID)
        ? s.collapsedSessions.filter((id) => id !== sessionID)
        : [...s.collapsedSessions, sessionID],
    })),
  setGroupBy: (groupBy) => set({ groupBy }),
  // Revealing a run acknowledges it, wherever the reveal came from: every
  // surface routes through this one call, so this is the only place the ack
  // belongs.
  navigate: (name, params = {}) => {
    set({ route: { name, params } })
    if (params.runId) get().ackRun(params.runId)
  },
})
