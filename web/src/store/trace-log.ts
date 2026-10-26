import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { Event } from '@/lib/types'
import type { RootStore } from '@/store'
import type { SliceCreator } from '@/store/slice'

export const traceEventTypes = ['run.status', 'run.cost']

const pageSize = 500
const readCap = 2000

export interface TraceLog {
  /** Oldest first. */
  events: Event[]
  loading: boolean
  /** A whole read has finished once, so the events are the run's history. */
  read: boolean
  /** The read stopped at its cap: older events, and the tokens and cost they add, are not held. */
  capped: boolean
  error: string | null
}

/** A run's Trace view state for this tab: folds, zoom, the row the keyboard is on, and older history pages read on the view's own. */
export interface TraceView {
  open: Record<string, boolean>
  /** The span the axis is zoomed to. */
  zoom?: string
  active?: string
  paged?: { generation?: number; count: number }
}

export interface TraceSlice {
  traceLogs: Record<string, TraceLog>
  traceViews: Record<string, TraceView>
  beginTraceLog: (runID: string) => void
  addTraceEvents: (runID: string, events: Event[]) => void
  /** An error leaves the log unread. */
  endTraceLog: (runID: string, error: string | null, capped?: boolean) => void
  /** Keeps a read log current from the live event stream. */
  appendTraceEvent: (event: Event) => void
  setTraceView: (runID: string, patch: Partial<TraceView>) => void
}

const emptyLog: TraceLog = { events: [], loading: false, read: false, capped: false, error: null }
export const emptyTraceView: TraceView = { open: {} }

function merge(current: Event[], incoming: Event[]): Event[] {
  const seen = new Set(current.map((event) => event.seq))
  const fresh = incoming.filter((event) => !seen.has(event.seq))
  return fresh.length ? [...current, ...fresh].sort((a, b) => a.seq - b.seq) : current
}

export const createTraceSlice: SliceCreator<TraceSlice> = (set, get) => ({
  traceLogs: {},
  traceViews: {},
  beginTraceLog: (runID) =>
    set((s) => ({ traceLogs: { ...s.traceLogs, [runID]: { ...(s.traceLogs[runID] ?? emptyLog), loading: true, error: null } } })),
  addTraceEvents: (runID, events) =>
    set((s) => {
      const log = s.traceLogs[runID] ?? emptyLog
      const merged = merge(log.events, events)
      return merged === log.events ? {} : { traceLogs: { ...s.traceLogs, [runID]: { ...log, events: merged } } }
    }),
  endTraceLog: (runID, error, capped = false) =>
    set((s) => {
      const log = s.traceLogs[runID] ?? emptyLog
      return { traceLogs: { ...s.traceLogs, [runID]: { ...log, loading: false, read: log.read || error === null, capped: log.capped || capped, error } } }
    }),
  appendTraceEvent: (event) => {
    if (!get().traceLogs[event.run_id] || !traceEventTypes.includes(event.type)) return
    get().addTraceEvents(event.run_id, [event])
  },
  setTraceView: (runID, patch) =>
    set((s) => ({ traceViews: { ...s.traceViews, [runID]: { ...(s.traceViews[runID] ?? emptyTraceView), ...patch } } })),
})

/** Reads the run's status and cost history, newest first up to a cap; a log already read catches up from its last event. */
export async function readTraceLog(store: RootStore, client: Api, run: { id: string; workspace_id: string }): Promise<void> {
  const state = store.getState()
  const log = state.traceLogs[run.id]
  if (log?.loading) return
  const after = log?.read ? log.events.at(-1)?.seq : undefined
  state.beginTraceLog(run.id)
  // A seq reset clears the logs; a page read before it must not land in the new one.
  const epoch = state.terminalCacheEpoch
  const current = () => store.getState().terminalCacheEpoch === epoch
  const query = { workspace_id: run.workspace_id, run_id: run.id, types: traceEventTypes, limit: pageSize }
  let capped = false
  try {
    if (after === undefined) {
      let before: number | undefined
      for (let read = 0; ;) {
        const page = await client.workspaceTimeline({ ...query, newest: true, before_seq: before })
        if (!current()) return
        store.getState().addTraceEvents(run.id, page.events)
        read += page.events.length
        before = page.older_seq
        if (!page.more || before === undefined) break
        capped = read >= readCap
        if (capped) break
      }
    } else {
      for (let cursor = after; ;) {
        const page = await client.workspaceTimeline({ ...query, after_seq: cursor })
        if (!current()) return
        store.getState().addTraceEvents(run.id, page.events)
        cursor = page.next_seq
        if (!page.more) break
      }
    }
    store.getState().endTraceLog(run.id, null, capped)
  } catch (err) {
    if (current()) store.getState().endTraceLog(run.id, message(err))
  }
}
