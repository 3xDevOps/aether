import type {
  LinkStatus,
  PullResult,
  SyncSessionStatus,
  UpdateStatus,
} from '@/lib/types'
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
  /**
   * What the last pull fetched, by run id. The verb lives in the run action
   * bar and the git output belongs on the diff tab, so the result waits here
   * for whichever surface shows it. Lasts as long as the tab.
   */
  pulls: Record<string, PullResult>
  recordPull: (runID: string, result: PullResult) => void
  /**
   * The last update.check answer; null until the banner host fetches it.
   * Which version the member has already dismissed is a view preference and
   * lives on the ui slice instead, because that one survives a reload.
   */
  update: UpdateStatus | null
  setUpdate: (update: UpdateStatus) => void
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
  pulls: {},
  recordPull: (runID, result) =>
    set((s) => ({ pulls: { ...s.pulls, [runID]: result } })),
  update: null,
  setUpdate: (update) => set({ update }),
})
