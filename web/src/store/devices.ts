import type { Api } from '@/lib/api'
import type { Device } from '@/lib/types'
import type { RootState, RootStore } from '@/store'
import { capability } from '@/store/hooks'
import type { SliceCreator } from '@/store/slice'

/** Every member's devices for an admin, the viewer's own for anyone else. */
export interface DevicesSlice {
  devices: Device[]
  /** The identityKey `devices` was read for. */
  devicesFor: string | null
  setDevices: (devices: Device[], viewer: string | null) => void
}

export const createDevicesSlice: SliceCreator<DevicesSlice> = (set) => ({
  devices: [],
  devicesFor: null,
  setDevices: (devices, viewer) => set({ devices, devicesFor: viewer }),
})

const reads = new WeakMap<RootStore, number>()

const listsDevices = (s: RootState) =>
  s.info !== null && capability(s.capabilities).hasMethod('member.device.list')

/** Only the latest read lands; a failed read, a read for another viewer, or
 * a viewer change while it waits clears the slice. */
export async function loadDevices(store: RootStore, client: Api): Promise<void> {
  const read = (reads.get(store) ?? 0) + 1
  reads.set(store, read)
  const s = store.getState()
  if (!listsDevices(s) || s.devicesFor !== s.identityKey) s.setDevices([], s.identityKey)
  if (!listsDevices(s)) return
  let devices: Device[] = []
  try {
    devices = await client.memberDeviceList()
  } finally {
    const now = store.getState()
    if (reads.get(store) === read) {
      now.setDevices(now.identityKey === s.identityKey && listsDevices(now) ? devices : [], now.identityKey)
    }
  }
}
