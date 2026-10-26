import type { Event } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

export type LaneWindow = '1h' | '6h' | '24h' | '7d'
export type LaneGroup = 'status' | 'owner'
export type LaneEdgeKind = 'orchestration' | 'dependency' | 'submission' | 'message' | 'overlap'

export const laneWindows: LaneWindow[] = ['1h', '6h', '24h', '7d']
export const laneEdgeKinds: LaneEdgeKind[] = ['orchestration', 'dependency', 'submission', 'message', 'overlap']

/** The event types Lanes reads: state changes for the bars and agent mail for the edges. */
export const laneEventTypes = ['run.status', 'coord.message', 'coord.message.acked']

/** Events held per workspace. A busy workspace's 7d window can hold far more; past this the bars say history is not loaded. */
export const historyBudget = 5000

/** One workspace's history, read backward from the log head. */
export interface LaneHistory {
  /** Oldest first, unique by seq. */
  events: Event[]
  /** The next page's `before_seq`; undefined once the log is exhausted. */
  olderSeq?: number
  /** The time of the oldest event read; -Infinity once nothing older exists. */
  floor: number
  loading: boolean
  error: string | null
  /** Paging stopped on its event budget with older history unread. */
  capped: boolean
}

export interface LanesSlice {
  laneHistory: Record<string, LaneHistory>
  /** Live events are kept only while a Lanes view is mounted. */
  laneViews: number
  /** Null follows the default: 1h while anything works, else 6h. */
  laneWindow: LaneWindow | null
  laneGroup: LaneGroup
  laneHidden: LaneEdgeKind[]
  holdLanes: () => () => void
  appendLaneEvent: (event: Event) => void
  setLaneHistory: (workspaceID: string, patch: Partial<LaneHistory>, older?: Event[]) => void
  setLaneWindow: (window: LaneWindow | null) => void
  setLaneGroup: (group: LaneGroup) => void
  setLaneHidden: (hidden: LaneEdgeKind[]) => void
}

export const emptyLaneHistory: LaneHistory = { events: [], floor: Infinity, loading: false, error: null, capped: false }

export function normalizeLaneWindow(value: unknown): LaneWindow | null {
  return laneWindows.includes(value as LaneWindow) ? (value as LaneWindow) : null
}

export function normalizeLaneHidden(value: unknown): LaneEdgeKind[] {
  return Array.isArray(value) ? laneEdgeKinds.filter((kind) => value.includes(kind)) : []
}

function merged(events: Event[], more: Event[]): Event[] {
  const seen = new Set(events.map((event) => event.seq))
  const fresh = more.filter((event) => !seen.has(event.seq))
  return fresh.length ? [...events, ...fresh].sort((a, b) => a.seq - b.seq) : events
}

export const createLanesSlice: SliceCreator<LanesSlice> = (set, get) => ({
  laneHistory: {},
  laneViews: 0,
  laneWindow: null,
  laneGroup: 'status',
  laneHidden: [],
  // The last view to leave drops the history: nothing appends to it unheld.
  holdLanes: () => {
    set((s) => ({ laneViews: s.laneViews + 1 }))
    return () => set((s) => (s.laneViews === 1 ? { laneViews: 0, laneHistory: {} } : { laneViews: s.laneViews - 1 }))
  },
  appendLaneEvent: (event) => {
    const s = get()
    if (s.laneViews === 0 || !laneEventTypes.includes(event.type)) return
    const history = s.laneHistory[event.workspace_id]
    if (!history) return
    if (history.events.some((e) => e.seq === event.seq)) return
    const events = merged(history.events, [event])
    const drop = events.length - historyBudget
    // Dropping the oldest moves the boundary up to the oldest kept, so bars before it read History not loaded.
    const next = drop > 0 ? { events: events.slice(drop), floor: Date.parse(events[drop].time), olderSeq: events[drop].seq } : { events }
    set((state) => ({
      laneHistory: { ...state.laneHistory, [event.workspace_id]: { ...history, ...next } },
    }))
  },
  setLaneHistory: (workspaceID, patch, older = []) =>
    set((s) => {
      const history = s.laneHistory[workspaceID] ?? emptyLaneHistory
      return {
        laneHistory: {
          ...s.laneHistory,
          [workspaceID]: { ...history, ...patch, events: merged(history.events, older) },
        },
      }
    }),
  setLaneWindow: (laneWindow) => set({ laneWindow }),
  setLaneGroup: (laneGroup) => set({ laneGroup }),
  setLaneHidden: (laneHidden) => set({ laneHidden }),
})
