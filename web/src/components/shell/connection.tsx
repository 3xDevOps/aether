import { StateLine } from '@/components/ui/status-dot'
import type { ConnectionState } from '@/lib/stream'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import type { UnreachableKind } from '@/store/server'

export const connectionLabel: Record<ConnectionState, string> = {
  connecting: 'Connecting',
  live: 'Live',
  reconnecting: 'Reconnecting',
  offline: 'Offline',
}

const connectionDot: Record<ConnectionState, string> = {
  connecting: 'bg-state-paused',
  live: 'bg-state-done',
  reconnecting: 'bg-state-needs-you',
  offline: 'bg-state-failed',
}

// Which hop is down decides what an operator does next.
export const unreachableLabel: Record<UnreachableKind, string> = {
  network: 'this computer is offline - reconnect to wifi or your VPN',
  gateway: 'dashboard gateway is gone - restart aether gui',
  tailnet: 'no answer from your server - check Tailscale and the server host',
  server: 'server unreachable over SSH - check the server and network; retrying',
  edge: 'edge unreachable - check this computer can reach it; retrying',
  'edge-server': 'server not connected to the edge - check aether-server edge status',
  'edge-refused': 'the edge refused the connection to this server',
  'signed-out': 'signed out of the edge - run aether login',
  'not-member': 'not a member of this server - ask an admin to invite you again',
  'device-revoked': 'this device was revoked on the server',
  'device-pending': 'this device is waiting for approval - approve its code from another device or on the server',
  refused: 'the gateway refused this device - it is not identified as a member',
  identity: 'the server cannot identify this device - check tailscaled on the server host',
}

/** The connection problem in one sentence, or null while the feed is live,
 * still making its first connection, or has no server to connect to. */
export function useConnectionProblem(): string | null {
  const connection = useStore((s) => s.connection)
  const unreachable = useStore((s) => s.unreachable)
  const noServer = useStore((s) => s.linkStatus?.server_configured === false)
  if (noServer) return null
  if (unreachable) return `${connectionLabel[connection]} · ${unreachableLabel[unreachable]}`
  if (connection === 'live' || connection === 'connecting') return null
  return connectionLabel[connection]
}

export function ConnectionLine({ className }: { className?: string }) {
  const problem = useConnectionProblem()
  if (!problem) return null
  return (
    <div className={cn('min-w-0', className)}>
      <StateLine tone="failed">{problem}</StateLine>
    </div>
  )
}

// Screen readers often skip a live region that mounts together with its
// text, so this one stays mounted and only its text changes.
export function ConnectionAnnouncer() {
  return (
    <div role="status" className="sr-only">
      {useConnectionProblem()}
    </div>
  )
}

export function ConnectionDot({ className }: { className?: string }) {
  const connection = useStore((s) => s.connection)
  return <span aria-hidden className={cn('size-2 shrink-0 rounded-full', connectionDot[connection], className)} />
}
