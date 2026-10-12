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

/** Lands only for the viewer it was read for. */
export async function loadEdge(store: RootStore, client: Api): Promise<void> {
  const s = store.getState()
  if (!capability(s.capabilities).hasMethod('server.edge')) {
    s.setEdge(null)
    return
  }
  const edge = await client.serverEdge()
  const now = store.getState()
  if (now.identityKey === s.identityKey) now.setEdge(edge)
}
