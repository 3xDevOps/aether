import type { Api } from '@/lib/api'
import type { Device } from '@/lib/types'
import type { RootState, RootStore } from '@/store'
import { capability } from '@/store/hooks'
import type { SliceCreator } from '@/store/slice'

/** Every member's devices, for an admin's Needs you; empty for anyone else. */
export interface DevicesSlice {
  devices: Device[]
  setDevices: (devices: Device[]) => void
}

export const createDevicesSlice: SliceCreator<DevicesSlice> = (set) => ({
  devices: [],
  setDevices: (devices) => set({ devices }),
})

const reads = new WeakMap<RootStore, number>()

const listsDevices = (s: RootState) =>
  s.info?.member.role === 'admin' && capability(s.capabilities).hasMethod('member.device.list')

/** Only the latest read lands, and only for the viewer it was made for. */
export async function loadDevices(store: RootStore, client: Api): Promise<void> {
  const read = (reads.get(store) ?? 0) + 1
  reads.set(store, read)
  const s = store.getState()
  if (!listsDevices(s)) {
    s.setDevices([])
    return
  }
  const devices = await client.memberDeviceList()
  const now = store.getState()
  if (reads.get(store) !== read || now.identityKey !== s.identityKey) return
  now.setDevices(listsDevices(now) ? devices : [])
}
