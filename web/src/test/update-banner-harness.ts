// The store and gateway the update banner tests start from, shared by the
// CLI and server banner suites so neither carries its own copy.

import { act } from '@testing-library/react'
import { FOCUS_READ_MS, RECHECK_MS } from '@/components/update-checks'
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
    connection: 'live',
    gatewayRestarting: false,
    serverUpdateError: null,
    serverUpdateProgressAt: 0,
    updateRequired: null,
    clientChecking: false,
    clientCheckError: null,
    serverChecking: false,
    clientApply: { name: 'idle' },
    clientBuild: null,
  })
}

/** The longest jittered re-check delay: advancing by it fires exactly one
 * periodic read, and by n of it n reads, for n up to 4. */
export const RECHECK_PERIOD = Math.ceil(RECHECK_MS * 1.1)

/** Lets the pending update.check promises settle. Needs fake timers. */
export async function settle() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0)
  })
}

/** A re-check through the focus read. Waiting out the periodic one would
 * also run every 30s clock tick and 1s rebuild poll in between, which is
 * slow enough under CI load to time the test out. Needs fake timers. */
export async function refocus() {
  await act(async () => {
    vi.setSystemTime(Date.now() + FOCUS_READ_MS)
    window.dispatchEvent(new Event('focus'))
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
