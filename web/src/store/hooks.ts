import { useMemo } from 'react'
import { pendingApprovalKey } from '@/lib/status'
import type { GatewayCapabilities, Member } from '@/lib/types'
import { useStore } from '@/store'
import {
  sidebarGroups,
  sidebarRuns,
  type SidebarGroup,
  type SidebarRun,
} from '@/store/selectors'

/**
 * The pending-approval run set, stable across inbox refetches that changed
 * nothing: the store subscription is on a string key, so an unchanged queue
 * neither re-renders subscribers nor invalidates downstream memos.
 */
export function usePendingApprovalRuns(): Set<string> {
  const key = useStore((s) => pendingApprovalKey(s.inbox))
  return useMemo(() => new Set(key ? key.split('\n') : []), [key])
}

function useSidebarInput() {
  const workspace = useStore((s) => s.activeWorkspace)
  const runs = useStore((s) => s.runs)
  const members = useStore((s) => s.members)
  const groupBy = useStore((s) => s.groupBy)
  const pending = usePendingApprovalRuns()
  return useMemo(
    () => ({ workspace, runs, members, groupBy, pending }),
    [workspace, runs, members, groupBy, pending],
  )
}

export function useSidebarGroups(): SidebarGroup[] {
  const input = useSidebarInput()
  return useMemo(() => sidebarGroups(input), [input])
}

/**
 * How many runs are waiting on a human right now - the count behind the
 * sidebar's badge. Stalls, clean exits and pending approvals all present
 * as needs-attention, so one number covers the whole notification path.
 */
export function useAttentionCount(): number {
  const input = useSidebarInput()
  return useMemo(
    () => sidebarRuns(input).filter((r) => r.state === 'needs-attention').length,
    [input],
  )
}

/** Every run in the active workspace, worst state and most recent change first. */
export function useAttentionRuns(): SidebarRun[] {
  const input = useSidebarInput()
  return useMemo(() => sidebarRuns(input), [input])
}

/** What the connected gateway can do, queryable per method, verb and socket. */
export interface Capability {
  hasMethod: (method: string) => boolean
  hasLocal: (verb: string) => boolean
  hasWS: (name: string) => boolean
}

/**
 * The fallback allowlist for a gateway whose /capabilities endpoint did not
 * answer. It is the read-and-steer set every gateway serves; the admin
 * surfaces stay hidden rather than rendering buttons that would fail, so an
 * unknown gateway degrades to monitoring instead of to "everything".
 */
const LEGACY_REMOTE_METHODS: Record<string, true> = {
  'server.info': true,
  'workspace.list': true,
  'workspace.get': true,
  'member.list': true,
  'run.launch': true,
  'run.list': true,
  'run.get': true,
  'run.kill': true,
  'run.pause': true,
  'run.resume': true,
  'run.inject': true,
  'run.close': true,
  'run.handoff': true,
  'approval.list': true,
  'approval.decide': true,
  'presence.roster': true,
  'presence.heartbeat': true,
  'workspace.timeline': true,
  'cost.report': true,
  'budget.get': true,
  'run.overlaps': true,
  'template.list': true,
  'template.launch': true,
}

/**
 * Answers from a capabilities result. Null means a legacy remote monitor
 * that predates the endpoint: it serves exactly the pre-capabilities
 * allowlist and both gateway sockets; the admin methods behind the newer
 * surfaces would 403, and local verbs are a desktop-gateway feature it
 * cannot have. A "*" methods entry means every method.
 */
export function capability(caps: GatewayCapabilities | null): Capability {
  if (caps === null) {
    return {
      hasMethod: (method) => LEGACY_REMOTE_METHODS[method] === true,
      hasLocal: () => false,
      hasWS: (name) => name === 'events' || name === 'attach',
    }
  }
  const every = caps.methods.includes('*')
  return {
    hasMethod: (method) => every || caps.methods.includes(method),
    hasLocal: (verb) => (caps.local ?? []).includes(verb),
    hasWS: (name) => caps.ws.includes(name),
  }
}

export function useCapability(): Capability {
  const caps = useStore((s) => s.capabilities)
  return useMemo(() => capability(caps), [caps])
}

/**
 * The caller's own role, or null before hydration. The gateway capability
 * descriptor answers what the transport can carry; this answers what this
 * member may do. An admin affordance needs both, because the local gateway
 * advertises every method regardless of who is behind it.
 */
export function useSelfRole(): Member['role'] | null {
  return useStore((s) => s.info?.member.role ?? null)
}

/** Whether the caller holds the admin role. False until hydrated. */
export function useIsAdmin(): boolean {
  return useStore((s) => s.info?.member.role === 'admin')
}
