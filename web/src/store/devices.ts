import type { Api } from '@/lib/api'
import type { Device } from '@/lib/types'
import type { RootStore } from '@/store'
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

export async function loadDevices(store: RootStore, client: Api): Promise<void> {
  const s = store.getState()
  if (s.info?.member.role !== 'admin' || !capability(s.capabilities).hasMethod('member.device.list')) {
    s.setDevices([])
    return
  }
  store.getState().setDevices(await client.memberDeviceList())
}
