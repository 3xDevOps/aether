import type { Api } from '@/lib/api'
import type { ServerEdge } from '@/lib/types'
import type { RootStore } from '@/store'
import { capability } from '@/store/hooks'
import type { SliceCreator } from '@/store/slice'

/** server.edge, for Settings › Remote access and the device cards; null
 * before it is read or when the server does not serve it. */
export interface EdgeSlice {
  edge: ServerEdge | null
  setEdge: (edge: ServerEdge | null) => void
}

export const createEdgeSlice: SliceCreator<EdgeSlice> = (set) => ({
  edge: null,
  setEdge: (edge) => set({ edge }),
})

const reads = new WeakMap<RootStore, number>()

/** Only the latest read lands; a failed read or one made for another viewer
 * clears the slice. */
export async function loadEdge(store: RootStore, client: Api): Promise<void> {
  const read = (reads.get(store) ?? 0) + 1
  reads.set(store, read)
  const s = store.getState()
  if (!capability(s.capabilities).hasMethod('server.edge')) {
    s.setEdge(null)
    return
  }
  let edge: ServerEdge | null = null
  try {
    edge = await client.serverEdge()
  } finally {
    const now = store.getState()
    if (reads.get(store) === read) now.setEdge(now.identityKey === s.identityKey ? edge : null)
  }
}
