import type { Event } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

/** What the feed is narrowed to. Empty strings mean no filter. */
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
  /** The window of history the feed is showing, oldest first. */
  feed: Event[]
  feedFilters: FeedFilters
  /**
   * Where the window starts and how far it has read. `feedFloor` is the
   * `after_seq` the window opened at - "load older" walks it back - and
   * `feedCursor` is the seq the next page resumes from, which the live
   * tail also uses.
   */
  feedFloor: number
  feedCursor: number
  /** History remains before the floor. */
  feedOlder: boolean
  /**
   * Stamps the current read. Every open bumps it, and a read whose stamp
   * has gone stale writes nothing: without it a read still paging under
   * the old filters would append its results into the new feed and drag
   * the cursor past a range the new filters were never asked about.
   */
  feedRequest: number
  feedLoading: boolean
  feedError: string | null
  /** The read stopped on its page budget with history still unread. */
  feedTruncated: boolean
  setFeedFilters: (filters: Partial<FeedFilters>) => void
  beginFeed: () => void
  resetFeed: (floor: number, older: boolean) => void
  extendFeed: (floor: number, older: boolean) => void
  appendFeed: (events: Event[], cursor: number) => void
  setFeedLoading: (loading: boolean, error?: string | null) => void
  setFeedTruncated: (truncated: boolean) => void
}

export const createTimelineSlice: SliceCreator<TimelineSlice> = (set) => ({
  feed: [],
  feedFilters: emptyFilters,
  feedFloor: 0,
  feedCursor: 0,
  feedOlder: false,
  feedRequest: 0,
  feedLoading: false,
  feedError: null,
  feedTruncated: false,
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
        // "Load older" delivers pages from before the window, so arrival
        // order is not log order: sorting keeps the oldest-first invariant
        // the view renders from.
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
