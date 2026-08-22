import type { LinkStatus, SyncSessionStatus } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

/**
 * What the local gateway last reported about this machine: the linked
 * repository and the background sync sessions. Both are polled from the
 * /local/v1 verbs by the views that show them; nothing here persists.
 */
export interface LocalSlice {
  /** Background sync sessions by run id, from sync.status. */
  syncSessions: Record<string, { state: string; conflict: string | null }>
  setSyncSessions: (sessions: SyncSessionStatus[]) => void
  /** The last link.status answer; null until fetched. */
  linkStatus: LinkStatus | null
  setLinkStatus: (status: LinkStatus) => void
}

export const createLocalSlice: SliceCreator<LocalSlice> = (set) => ({
  syncSessions: {},
  setSyncSessions: (sessions) =>
    set({
      syncSessions: Object.fromEntries(
        sessions.map((s) => [s.run_id, { state: s.state, conflict: s.conflict }]),
      ),
    }),
  linkStatus: null,
  setLinkStatus: (linkStatus) => set({ linkStatus }),
})
