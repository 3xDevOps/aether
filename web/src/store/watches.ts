import { ApiError, codeNotFound, type Api } from '@/lib/api'
import type { Watch, WatchGetResult, WatchNotification } from '@/lib/types'
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

const reads = new WeakMap<RootStore, number>()

export const listsWatches = (s: RootState) => s.info !== null && capability(s.capabilities).hasMethod('watch.list')

/** Only the latest read lands; a read for another viewer, or a viewer change while it waits, clears the slice. */
export async function loadWatches(store: RootStore, client: Api): Promise<void> {
  const read = (reads.get(store) ?? 0) + 1
  reads.set(store, read)
  const s = store.getState()
  if (!listsWatches(s) || s.watchesFor !== s.identityKey) s.setWatches([], s.identityKey)
  if (!listsWatches(s)) return
  const watches = await client.watchList()
  const now = store.getState()
  if (reads.get(store) === read && now.identityKey === s.identityKey) now.setWatches(watches, now.identityKey)
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
  try {
    const result = await client.watchGet(watchID)
    if (store.getState().identityKey === viewer) store.getState().putWatch(result.watch, result.notifications)
    return result
  } catch (err) {
    if (err instanceof ApiError && err.code === codeNotFound) {
      store.getState().dropWatch(watchID)
      return null
    }
    throw err
  }
}
