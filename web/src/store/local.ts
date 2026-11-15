import type {
  LinkStatus,
  PullResult,
  SyncSessionStatus,
  UpdateStatus,
} from '@/lib/types'
import { clearLaunchDraft, launchDraftLinkStatusPending, readLaunchDraft } from '@/components/launch/draft'
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
   * What the last pull fetched, by run id. The verb lives in the run's More
   * menu and the git output belongs on the Changes view, so the result waits here
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
  /**
   * Set while the Update button's install is running. The periodic release
   * re-check stands down for it: that read answers from the cache the click
   * just refreshed, so it could rename the banner over the release being
   * installed behind it.
   */
  installingUpdate: boolean
  setInstallingUpdate: (installingUpdate: boolean) => void
  /**
   * Set once an in-app update tells the gateway it is going away and coming
   * back (`update.apply` answering `restarting`). Never cleared: the page
   * that reads it is on its way out regardless of how the reconnect goes.
   * An unsupervised gateway never sets it - that tab keeps its gateway, and
   * a flag that is never cleared would hide a real disconnect later.
   */
  gatewayRestarting: boolean
  setGatewayRestarting: (gatewayRestarting: boolean) => void
}

export const createLocalSlice: SliceCreator<LocalSlice> = (set, get) => ({
  syncSessions: {},
  setSyncSessions: (sessions) =>
    set({
      syncSessions: Object.fromEntries(
        sessions.map((s) => [s.run_id, { state: s.state, conflict: s.conflict }]),
      ),
    }),
  linkStatus: null,
  setLinkStatus: (linkStatus) => {
    const state = get()
    if (linkStatus.edge_url) {
      const error = clearLaunchDraft(state.identityKey)
      set({
        linkStatus,
        launchDraftError: error ? `Could not remove launch draft from this browser: ${error}` : null,
      })
      return
    }
    if (state.launchDraftError === launchDraftLinkStatusPending && state.identityKey && state.paletteDialog !== 'launch' && !state.launchDraft) {
      const restored = readLaunchDraft(state.identityKey)
      set({ linkStatus, launchDraft: restored.draft, launchDraftError: restored.error })
      return
    }
    set({ linkStatus })
  },
  pulls: {},
  recordPull: (runID, result) =>
    set((s) => ({ pulls: { ...s.pulls, [runID]: result } })),
  update: null,
  setUpdate: (update) => set({ update }),
  installingUpdate: false,
  setInstallingUpdate: (installingUpdate) => set({ installingUpdate }),
  gatewayRestarting: false,
  setGatewayRestarting: (gatewayRestarting) => set({ gatewayRestarting }),
})
