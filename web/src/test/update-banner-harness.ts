// The store and gateway the update banner tests start from, shared by the
// CLI and server banner suites so neither carries its own copy.

import type { GatewayCapabilities, Member } from '@/lib/types'
import { useStore } from '@/store'
import type { UpdateKind } from '@/store/ui'
import { alice, bob, serverInfo } from '@/test/fixtures'

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
    serverUpdate: null,
    serverUpdateProgress: null,
    members: { [alice.id]: alice, [bob.id]: bob },
    hydrated: true,
    gatewayRestarting: false,
  })
}
