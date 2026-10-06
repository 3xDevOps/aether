import type { CoordMessagePayload, Event } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

/** Empty strings mean no filter. */
export interface FeedFilters {
  workspaceID: string
  runID: string
  memberID: string
  type: string
}

export const emptyFilters: FeedFilters = {
  workspaceID: '',
  runID: '',
  memberID: '',
  type: '',
}

export interface TimelineSlice {
  /** Oldest first. */
  feed: Event[]
  feedFilters: FeedFilters
  /**
   * `feedFloor` is the `after_seq` the window opened at ("load older" walks it
   * back); `feedCursor` is where the next page and the live tail resume.
   */
  feedFloor: number
  feedCursor: number
  /** History remains before the floor. */
  feedOlder: boolean
  /** Bumped per open; a read with a stale stamp writes nothing, so old-filter pages never land in the new feed. */
  feedRequest: number
  feedLoading: boolean
  feedError: string | null
  /** The read stopped on its page budget with history still unread. */
  feedTruncated: boolean
  /** Live events are added only while a feed view is mounted. */
  feedViews: number
  holdFeed: () => () => void
  /** Adds a live event the filters select, as the server's reader would page it. */
  appendLiveEvent: (event: Event) => void
  setFeedFilters: (filters: Partial<FeedFilters>) => void
  beginFeed: () => void
  resetFeed: (floor: number, older: boolean) => void
  extendFeed: (floor: number, older: boolean) => void
  appendFeed: (events: Event[], cursor: number) => void
  setFeedLoading: (loading: boolean, error?: string | null) => void
  setFeedTruncated: (truncated: boolean) => void
}

export const createTimelineSlice: SliceCreator<TimelineSlice> = (set, get) => ({
  feed: [],
  feedFilters: emptyFilters,
  feedFloor: 0,
  feedCursor: 0,
  feedOlder: false,
  feedRequest: 0,
  feedLoading: false,
  feedError: null,
  feedTruncated: false,
  feedViews: 0,
  holdFeed: () => {
    set((s) => ({ feedViews: s.feedViews + 1 }))
    return () => set((s) => ({ feedViews: s.feedViews - 1 }))
  },
  appendLiveEvent: (event) => {
    // Even an empty set notifies listeners and makes persist rewrite localStorage.
    const now = get()
    if (now.feedViews === 0 || !selects(now.feedFilters, event)) return
    if (now.feed.some((e) => e.seq === event.seq)) return
    set((s) => {
      const last = s.feed.at(-1)
      return {
        feed: !last || last.seq < event.seq
          ? [...s.feed, event]
          : [...s.feed, event].sort((a, b) => a.seq - b.seq),
        // The cursor may skip ahead only while the window is whole: a read in
        // flight, failed or cut short still has history before this event.
        feedCursor: s.feedLoading || s.feedError || s.feedTruncated
          ? s.feedCursor
          : Math.max(s.feedCursor, event.seq),
      }
    })
  },
  setFeedFilters: (filters) =>
    set((s) => ({ feedFilters: { ...s.feedFilters, ...filters } })),
  beginFeed: () =>
    set((s) => ({
      feedRequest: s.feedRequest + 1,
      feedLoading: true,
      feedError: null,
      feedTruncated: false,
    })),
  resetFeed: (floor, older) =>
    set({ feed: [], feedFloor: floor, feedCursor: floor, feedOlder: older }),
  extendFeed: (floor, older) => set({ feedFloor: floor, feedOlder: older }),
  appendFeed: (events, cursor) =>
    set((s) => {
      const seen = new Set(s.feed.map((e) => e.seq))
      const fresh = events.filter((e) => !seen.has(e.seq))
      return {
        // "Load older" pages arrive out of log order; keep the feed oldest first.
        feed: fresh.length
          ? [...s.feed, ...fresh].sort((a, b) => a.seq - b.seq)
          : s.feed,
        feedCursor: Math.max(s.feedCursor, cursor),
      }
    }),
  setFeedLoading: (feedLoading, feedError = null) =>
    set({ feedLoading, feedError }),
  setFeedTruncated: (feedTruncated) => set({ feedTruncated }),
})

/** Per-run firehoses the server's reader leaves out unless asked for by type. */
const detailTypes = new Set(['run.diff', 'run.title', 'run.agent'])

function mailTo(event: Event, runID: string): boolean {
  return event.type === 'coord.message' && (event.payload as CoordMessagePayload | undefined)?.to_run_id === runID
}

/** The server reader's filter (`internal/timeline`), applied to one event. */
function selects(f: FeedFilters, event: Event): boolean {
  if (!f.workspaceID || event.workspace_id !== f.workspaceID) return false
  if (f.runID && event.run_id !== f.runID && !mailTo(event, f.runID)) return false
  if (f.memberID && event.actor_id !== f.memberID) return false
  return f.type ? event.type === f.type : !detailTypes.has(event.type)
}
