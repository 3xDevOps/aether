// The store and gateway the update banner tests start from, shared by the
// CLI and server banner suites so neither carries its own copy.

import { act } from '@testing-library/react'
import type { GatewayCapabilities, Member, UpdateStatus } from '@/lib/types'
import { useStore } from '@/store'
import type { UpdateKind } from '@/store/ui'
import { alice, bob, serverInfo, updateStatus } from '@/test/fixtures'

/** The desktop gateway's descriptor, carrying the update verbs. */
export function caps(over: Partial<GatewayCapabilities> = {}): GatewayCapabilities {
  return {
    gateway: 'local',
    methods: ['*'],
    ws: ['events', 'attach', 'terminal'],
    local: ['link.status', 'update.check', 'update.apply'],
    version: 'v1.2.3',
    ...over,
  }
}

export function seed(
  over: {
    self?: Member
    capabilities?: GatewayCapabilities
    dismissedUpdates?: Record<UpdateKind, string>
  } = {},
) {
  useStore.setState({
    info: { ...serverInfo, member: over.self ?? alice },
    capabilities: over.capabilities ?? caps(),
    dismissedUpdates: over.dismissedUpdates ?? { cli: '', server: '', shell: '' },
    update: null,
    installingUpdate: false,
    serverUpdate: null,
    serverUpdateProgress: null,
    members: { [alice.id]: alice, [bob.id]: bob },
    hydrated: true,
    gatewayRestarting: false,
  })
}

/** Lets the pending update.check promises settle. Needs fake timers. */
export async function settle() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0)
  })
}

/** update.check serving the seeded release from its cache, and `next` only
 * to a read that asks it to refresh. */
export function cachedUntilRefreshed(
  next: string,
): (refresh?: boolean) => Promise<UpdateStatus> {
  const status = updateStatus()
  return vi.fn(async (refresh?: boolean) =>
    refresh ? withLatest(status, next) : status,
  )
}

/** The seeded release with a different latest tag. */
export function withLatest(status: UpdateStatus, latest: string): UpdateStatus {
  return { ...status, cli: { ...status.cli, latest } }
}
