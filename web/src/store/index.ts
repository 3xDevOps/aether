import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import { createApprovalsSlice, type ApprovalsSlice } from '@/store/approvals'
import { createBoardSlice, type BoardSlice } from '@/store/board'
import { createCostSlice, type CostSlice } from '@/store/cost'
import { createDiffSlice, type DiffSlice } from '@/store/diff'
import { createLocalSlice, type LocalSlice } from '@/store/local'
import { createMembersSlice, type MembersSlice } from '@/store/members'
import { createPaletteSlice, type PaletteSlice } from '@/store/palette'
import { createPresenceSlice, type PresenceSlice } from '@/store/presence'
import { createRunsSlice, type RunsSlice } from '@/store/runs'
import { createServerSlice, type ServerSlice } from '@/store/server'
import { createSessionsSlice, type SessionsSlice } from '@/store/sessions'
import { createShellSlice, type ShellSlice } from '@/store/shell'
import { createTerminalSlice, type TerminalSlice } from '@/store/terminal'
import { createTimelineSlice, type TimelineSlice } from '@/store/timeline'
import { createUiSlice, type UiSlice } from '@/store/ui'

export type RootState = ServerSlice &
  SessionsSlice &
  RunsSlice &
  MembersSlice &
  TerminalSlice &
  BoardSlice &
  PaletteSlice &
  ApprovalsSlice &
  PresenceSlice &
  CostSlice &
  TimelineSlice &
  DiffSlice &
  ShellSlice &
  LocalSlice &
  UiSlice

/**
 * The root store: one Zustand store composed of independent slices. A new
 * feature adds a slice file and one line here.
 */
export function createRootStore() {
  return create<RootState>()(
    persist(
      (...a) => ({
        ...createServerSlice(...a),
        ...createSessionsSlice(...a),
        ...createRunsSlice(...a),
        ...createMembersSlice(...a),
        ...createTerminalSlice(...a),
        ...createBoardSlice(...a),
        ...createPaletteSlice(...a),
        ...createApprovalsSlice(...a),
        ...createPresenceSlice(...a),
        ...createCostSlice(...a),
        ...createTimelineSlice(...a),
        ...createDiffSlice(...a),
        ...createShellSlice(...a),
        ...createLocalSlice(...a),
        ...createUiSlice(...a),
      }),
      {
        name: 'aether.ui',
        // Only view preferences survive a reload; server data is re-hydrated.
        partialize: (s) => ({
          theme: s.theme,
          sidebarWidth: s.sidebarWidth,
          sidebarCollapsed: s.sidebarCollapsed,
          collapsedSessions: s.collapsedSessions,
          groupBy: s.groupBy,
          showIdle: s.showIdle,
        }),
      },
    ),
  )
}

export type RootStore = ReturnType<typeof createRootStore>

export const useStore = createRootStore()
