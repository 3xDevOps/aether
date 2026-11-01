import { useCallback, useEffect, useMemo, useState } from 'react'
import { useShallow } from 'zustand/react/shallow'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import type { AgentLoad } from '@/routes/board/lanes/model'
import { sameIdentity, useStore } from '@/store'
import { emptyLaneHistory, historyBudget, laneEventTypes, type LaneHistory } from '@/store/lanes'
import { sessionStreamOpen } from '@/store/session-stream'
import { itemsOf } from '@/store/sessions'
import { indexItems, noAgents } from '@/store/subagents'

const pageSize = 1000

/** The floor each workspace's view wants now; a read in flight follows it when the window moves. */
const wanted = new Map<string, number>()
/** The read that owns each workspace's history; a read superseded after a reset stops writing. */
const reads = new Map<string, object>()

/** Pages one workspace's history backward until it reaches the wanted floor, its budget, or the start of the log. */
async function readBack(workspaceID: string, floor: number): Promise<void> {
  wanted.set(workspaceID, floor)
  const current = useStore.getState().laneHistory[workspaceID]
  if (current && (current.loading || current.error || current.capped || current.floor <= floor)) return
  const read = {}
  reads.set(workspaceID, read)
  const owns = () => reads.get(workspaceID) === read
  const same = sameIdentity()
  const { setLaneHistory } = useStore.getState()
  setLaneHistory(workspaceID, { loading: true })
  try {
    for (;;) {
      const history = useStore.getState().laneHistory[workspaceID]
      if (!history || !owns() || !same()) return
      if (history.floor <= (wanted.get(workspaceID) ?? floor)) break
      if (history.events.length >= historyBudget) {
        setLaneHistory(workspaceID, { capped: true })
        break
      }
      const page = await api.workspaceTimeline({
        workspace_id: workspaceID,
        types: laneEventTypes,
        newest: true,
        before_seq: history.olderSeq,
        limit: pageSize,
      })
      const latest = useStore.getState().laneHistory[workspaceID]
      if (!latest || !owns() || !same()) return
      // Live events past the budget dropped the oldest held events during this read, so the page no longer meets them.
      if (latest.olderSeq !== history.olderSeq) continue
      const oldest = page.events[0] ? Date.parse(page.events[0].time) : history.floor
      setLaneHistory(
        workspaceID,
        { olderSeq: page.more ? page.older_seq : undefined, floor: page.more ? Math.min(history.floor, oldest) : -Infinity },
        page.events,
      )
    }
    setLaneHistory(workspaceID, { loading: false })
  } catch (err) {
    if (useStore.getState().laneHistory[workspaceID] && owns() && same()) setLaneHistory(workspaceID, { loading: false, error: message(err) })
  } finally {
    if (reads.get(workspaceID) === read) reads.delete(workspaceID)
  }
}

/** Holds lane history for the workspaces on screen and reads back to `floor`. */
export function useLaneHistory(workspaceIDs: string[], floor: number) {
  useEffect(() => useStore.getState().holdLanes(), [])
  const key = workspaceIDs.join('\n')
  // A server reset or a dropped hold clears history under a mounted view; read it back.
  const missing = useStore((s) => workspaceIDs.some((id) => !s.laneHistory[id]))
  useEffect(() => {
    for (const id of key ? key.split('\n') : []) void readBack(id, floor)
  }, [key, floor, missing])
  const histories = useStore(useShallow((s) => workspaceIDs.map((id) => s.laneHistory[id] ?? emptyLaneHistory)))
  const retry = useCallback(() => {
    for (const id of key ? key.split('\n') : []) {
      if (!useStore.getState().laneHistory[id]?.error) continue
      useStore.getState().setLaneHistory(id, { error: null })
      void readBack(id, floor)
    }
  }, [key, floor])
  return useMemo(() => {
    const ids = key ? key.split('\n') : []
    return {
      byWorkspace: new Map<string, LaneHistory>(ids.map((id, i) => [id, histories[i]])),
      events: histories.flatMap((h) => h.events),
      loading: histories.some((h) => h.loading),
      error: histories.find((h) => h.error)?.error ?? null,
      capped: histories.some((h) => h.capped),
      retry,
    }
  }, [key, histories, retry])
}

/** `resident` reads the session's live index; a session's store entry outlives its stream, so only an open stream counts. */
type Load = AgentLoad | { status: 'resident' }

/** Subagent sub-lanes: a resident Enhanced session's live index, or one history page read on demand. */
export function useLaneAgents() {
  const [loads, setLoads] = useState<Record<string, Load>>({})
  const ids = useMemo(() => Object.keys(loads), [loads])
  const resident = useStore(useShallow((s) => ids.map((id) => (loads[id].status === 'resident' ? s.acpSessions[id] : undefined))))
  const load = useCallback(async (runID: string) => {
    setLoads((current) => ({ ...current, [runID]: { status: 'loading' } }))
    try {
      const page = await api.runACPHistory(runID, undefined, pageSize)
      const items = itemsOf(page.frames)
      const partial = (items[0]?.seq ?? 0) > (page.oldest_seq ?? 1)
      const { index } = indexItems(noAgents, items)
      setLoads((current) => (runID in current ? { ...current, [runID]: { status: 'ready', index, partial } } : current))
    } catch (err) {
      setLoads((current) => (runID in current ? { ...current, [runID]: { status: 'error', error: message(err) } } : current))
    }
  }, [])
  const toggle = useCallback((runID: string, open: boolean) => {
    if (!open) {
      setLoads(({ [runID]: _, ...rest }) => rest)
      return
    }
    if (sessionStreamOpen(runID)) setLoads((current) => ({ ...current, [runID]: { status: 'resident' } }))
    else void load(runID)
  }, [load])
  const agents = useMemo(() => {
    const out: Record<string, AgentLoad> = {}
    ids.forEach((id, i) => {
      const load = loads[id]
      if (load.status !== 'resident') out[id] = load
      else out[id] = resident[i] ? { status: 'ready', index: resident[i].agents, partial: resident[i].more } : { status: 'loading' }
    })
    return out
  }, [ids, resident, loads])
  return { agents, toggle, retry: load }
}
