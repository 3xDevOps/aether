import { ApiError, codeNotFound, type Api } from '@/lib/api'
import type { Watch, WatchCreateParams, WatchGetResult, WatchNotification, WatchResult, WatchUpdateParams } from '@/lib/types'
import { isLive } from '@/lib/watches'
import type { RootState, RootStore } from '@/store'
import { capability } from '@/store/hooks'
import type { SliceCreator } from '@/store/slice'

/** The viewer's own watches; the server lists no one else's, admins included. */
export interface WatchesSlice {
  watches: Record<string, Watch>
  /** The identityKey `watches` was read for; null before the first read. */
  watchesFor: string | null
  /** Each read watch's notices, newest first, as watch.get last returned them. */
  watchNotices: Record<string, WatchNotification[]>
  setWatches: (watches: Watch[], viewer: string | null) => void
  putWatch: (watch: Watch, notices?: WatchNotification[]) => void
  dropWatch: (watchID: string) => void
}

export const createWatchesSlice: SliceCreator<WatchesSlice> = (set) => ({
  watches: {},
  watchesFor: null,
  watchNotices: {},
  setWatches: (watches, viewer) =>
    set((s) => ({
      watches: Object.fromEntries(watches.map((watch) => [watch.id, watch])),
      watchNotices: viewer === s.watchesFor ? s.watchNotices : {},
      watchesFor: viewer,
    })),
  putWatch: (watch, notices) =>
    set((s) => ({
      watches: { ...s.watches, [watch.id]: watch },
      ...(notices ? { watchNotices: { ...s.watchNotices, [watch.id]: notices } } : {}),
    })),
  dropWatch: (watchID) =>
    set((s) => {
      const { [watchID]: _, ...watches } = s.watches
      const { [watchID]: __, ...watchNotices } = s.watchNotices
      return { watches, watchNotices }
    }),
})

// A read or change takes its ticket when it is sent, a delete when its answer
// arrives. A watch's fields land only over older fields and lists, its
// notices only over older notices.
interface Order { next: number; list: number; cleared: number; watch: Map<string, number>; notices: Map<string, number> }
const orders = new WeakMap<RootStore, Order>()

function order(store: RootStore): Order {
  let o = orders.get(store)
  if (!o) orders.set(store, (o = { next: 0, list: 0, cleared: 0, watch: new Map(), notices: new Map() }))
  return o
}

const ticket = (store: RootStore) => ++order(store).next

function land(store: RootStore, viewer: string | null, at: number, watchID: string, watch: Watch | null, notices?: WatchNotification[]) {
  const s = store.getState()
  const o = order(store)
  if (s.identityKey !== viewer) return
  const fields = at > Math.max(o.watch.get(watchID) ?? 0, o.list)
  if (fields) o.watch.set(watchID, at)
  if (!watch) {
    if (fields) s.dropWatch(watchID)
    return
  }
  const kept = fields ? watch : s.watches[watchID]
  const fresh = notices !== undefined && kept !== undefined && at > (o.notices.get(watchID) ?? 0)
  if (fresh) o.notices.set(watchID, at)
  if (kept && (fields || fresh)) s.putWatch(kept, fresh ? notices : undefined)
}

export const listsWatches = (s: RootState) => s.info !== null && capability(s.capabilities).hasMethod('watch.list')

/** A read for another viewer, or a viewer change while it waits, clears the slice; a watch changed since the read was sent keeps its newer state. */
export async function loadWatches(store: RootStore, client: Api): Promise<void> {
  const at = ticket(store)
  const s = store.getState()
  if (!listsWatches(s) || s.watchesFor !== s.identityKey) {
    order(store).cleared = at
    s.setWatches([], s.identityKey)
  }
  if (!listsWatches(s)) return
  const watches = await client.watchList()
  const now = store.getState()
  const o = order(store)
  if (now.identityKey !== s.identityKey || at < Math.max(o.list, o.cleared)) return
  o.list = at
  const changed = (id: string) => (o.watch.get(id) ?? 0) > at
  now.setWatches([
    ...watches.filter((watch) => !changed(watch.id)),
    ...Object.values(now.watches).filter((watch) => changed(watch.id)),
  ], now.identityKey)
}

/** The live watch on a run, if the viewer has one. */
export function runWatch(s: RootState, runID: string): Watch | undefined {
  for (const watch of Object.values(s.watches)) {
    if (watch.target.kind === 'run' && watch.target.run_id === runID && isLive(watch)) return watch
  }
  return undefined
}

/** Reads one watch and its notices into the store; a watch the server no longer has leaves it. */
export async function readWatch(store: RootStore, client: Api, watchID: string): Promise<WatchGetResult | null> {
  const viewer = store.getState().identityKey
  const at = ticket(store)
  try {
    const result = await client.watchGet(watchID)
    land(store, viewer, at, watchID, result.watch, result.notifications)
    return result
  } catch (err) {
    if (err instanceof ApiError && err.code === codeNotFound) {
      land(store, viewer, at, watchID, null)
      return null
    }
    throw err
  }
}

/** Null when the viewer changed before the server answered: the answer is the previous viewer's. */
async function changeWatch(store: RootStore, send: () => Promise<WatchResult>): Promise<WatchResult | null> {
  const viewer = store.getState().identityKey
  const at = ticket(store)
  const result = await send()
  if (store.getState().identityKey !== viewer) return null
  land(store, viewer, at, result.watch.id, result.watch)
  return result
}

export const createWatch = (store: RootStore, client: Api, params: WatchCreateParams) => changeWatch(store, () => client.watchCreate(params))

export const updateWatch = (store: RootStore, client: Api, params: WatchUpdateParams) => changeWatch(store, () => client.watchUpdate(params))

export async function deleteWatch(store: RootStore, client: Api, watchID: string): Promise<void> {
  const viewer = store.getState().identityKey
  await client.watchDelete(watchID)
  land(store, viewer, ticket(store), watchID, null)
}
