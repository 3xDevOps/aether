import { StateLine } from '@/components/ui/status-dot'
import { edgeHost } from '@/lib/format'
import { isApprovalCode, shellQuote } from '@/lib/shell'
import type { ConnectionState } from '@/lib/stream'
import type { EdgePath, LinkStatus } from '@/lib/types'
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

/** Which of the four connectivity layers a failure belongs to. The gateway
 * hop is the desktop's own process, in front of all four. */
const unreachableLayer: Record<UnreachableKind, string> = {
  network: 'Client to edge',
  gateway: 'Dashboard gateway',
  tailnet: 'Client to server',
  server: 'End-to-end',
  edge: 'Client to edge',
  'edge-server': 'Server registration',
  'edge-not-claimed': 'Server registration',
  'edge-refused': 'Authorization',
  'signed-out': 'Authorization',
  'not-member': 'Authorization',
  'device-revoked': 'Authorization',
  'device-pending': 'Authorization',
  'server-refused': 'Authorization',
  refused: 'Authorization',
  identity: 'Authorization',
}

/** The gateway's name for a layer outranks the client's guess from the kind. */
const pathLayer: Record<EdgePath['layer'], string> = {
  'client-edge': 'Client to edge',
  authorization: 'Authorization',
  'server-registration': 'Server registration',
  'end-to-end': 'End-to-end',
}

/** What failed, on which server, and the command that comes next. */
export function unreachableSentence(
  kind: UnreachableKind,
  link: Pick<LinkStatus, 'addr' | 'edge_url' | 'server_id'> | null,
  approvalCode: string | null,
  at: Partial<EdgePath> | null = null,
): string {
  const edge = at?.edge || (link?.edge_url ? edgeHost(link.edge_url) : null)
  const id = at?.server_id || link?.server_id || link?.addr
  const server = id ? `server ${id}` : 'the server'
  const sentence = kindSentence(kind, edge, server, link, approvalCode)
  if (!at?.online?.length) return sentence
  const online = at.online.map((s) => (s.name ? `${s.name} (${s.id})` : s.id)).join(', ')
  return `${sentence}; ${edge ?? 'the edge'} sees ${online} online, and this dashboard is linked to ${server}`
}

function kindSentence(
  kind: UnreachableKind,
  edge: string | null,
  server: string,
  link: Pick<LinkStatus, 'edge_url'> | null,
  approvalCode: string | null,
): string {
  switch (kind) {
    case 'network':
      return 'this computer is offline - reconnect to wifi or your VPN'
    case 'gateway':
      return 'dashboard gateway is gone - restart aether gui'
    case 'tailnet':
      return `no answer from ${server} - check Tailscale and the server host`
    case 'server':
      return edge
        ? `${server} did not answer through ${edge} - run sudo aether-server edge status on that host; retrying`
        : `${server} did not answer over SSH - check the server and network; retrying`
    case 'edge':
      return `${edge ?? 'the edge'} did not answer - check this computer can reach it; retrying`
    case 'edge-server':
      return `${edge ?? 'the edge'} has no live registration for ${server} - compare with sudo aether-server edge status on that host`
    case 'edge-not-claimed':
      return `${server} is connected to ${edge ?? 'the edge'} and is finishing its claim - retrying`
    case 'edge-refused':
      return `${edge ?? 'the edge'} refused the connection to ${server}`
    case 'signed-out':
      return `signed out of ${edge ?? 'the edge'} - run ${link?.edge_url ? `aether login --edge ${link.edge_url}` : 'aether login'}`
    case 'not-member':
      return `not a member of ${server} - ask an admin to invite you again`
    case 'device-revoked':
      return `${server} revoked this device`
    case 'device-pending':
      return isApprovalCode(approvalCode)
        ? `${server} has not approved this device - run sudo aether-server device approve ${shellQuote(approvalCode)} on that host`
        : `${server} has not approved this device - an admin approves it from Needs you, or you do from an approved device`
    case 'server-refused':
      return `${server} refused this device`
    case 'refused':
      return 'the gateway refused this device - it is not identified as a member'
    case 'identity':
      return `${server} cannot identify this device - check tailscaled on the server host`
  }
}

/** The connection problem in one sentence, or null while the feed is live,
 * still making its first connection, or has no server to connect to. A
 * failed request outranks a live feed: the feed going live does not prove
 * reads reach the server, so the latest failure is shown on its own. */
export function useConnectionProblem(): string | null {
  const connection = useStore((s) => s.connection)
  const unreachable = useStore((s) => s.unreachable)
  const at = useStore((s) => s.unreachableAt)
  const approvalCode = useStore((s) => s.approvalCode)
  const link = useStore((s) => s.linkStatus)
  if (link?.server_configured === false) return null
  if (unreachable) {
    const layer = at?.layer ? pathLayer[at.layer] : unreachableLayer[unreachable]
    return `${layer} failed · ${unreachableSentence(unreachable, link, approvalCode, at)}`
  }
  if (connection === 'live' || connection === 'connecting') return null
  return connectionLabel[connection]
}

/** The footer's one word: a failed request is never called Live. */
export function useConnectionWord(): string {
  const connection = useStore((s) => s.connection)
  const unreachable = useStore((s) => s.unreachable !== null)
  return unreachable ? 'Unreachable' : connectionLabel[connection]
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
  const unreachable = useStore((s) => s.unreachable !== null)
  return <span aria-hidden className={cn('size-2 shrink-0 rounded-full', unreachable ? connectionDot.offline : connectionDot[connection], className)} />
}
