import { useMemo } from 'react'
import { pendingApprovalKey } from '@/lib/status'
import type { GatewayCapabilities, Member, Run } from '@/lib/types'
import { unansweredQuestions } from '@/store/collaboration'
import { approvalsForRun } from '@/store/approvals'
import { isArchivable } from '@/store/runs'
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
  return useMemo(
    () => ({ workspace, runs, members, groupBy }),
    [workspace, runs, members, groupBy],
  )
}

export function useSidebarGroups(): SidebarGroup[] {
  const input = useSidebarInput()
  return useMemo(() => sidebarGroups(input), [input])
}

/** Runs with a genuine unresolved request, independent of execution. */
export function useAttentionCount(): number {
  const input = useSidebarInput()
  const pending = usePendingApprovalRuns()
  const roomMessages = useStore((s) => s.roomMessages)
  return useMemo(() => {
    let count = 0
    for (const run of Object.values(input.runs)) {
      if (input.workspace && run.workspace_id !== input.workspace) continue
      if (run.archived_at && isArchivable(run.status)) continue
      const questions = run.unanswered_questions ??
        unansweredQuestions(roomMessages[run.id] ?? []).length
      if (pending.has(run.id) || (run.pending_inputs?.length ?? 0) > 0 || questions > 0) count++
    }
    return count
  }, [input, pending, roomMessages])
}

export function useRunInput(run: Run) {
  const approvals = useStore((s) => approvalsForRun(s.inbox, run.id).length)
  const questions = useStore((s) => run.unanswered_questions ??
    unansweredQuestions(s.roomMessages[run.id] ?? []).length)
  const requests = run.pending_inputs ?? []
  const native = requests.length
  const parts: string[] = []
  for (const [kind, label] of [
    ['question', 'question'],
    ['permission', 'permission request'],
    ['form', 'form'],
    ['extension_ui', 'extension dialog'],
  ] as const) {
    const count = requests.filter((request) => request.kind === kind).length
    if (count > 0) parts.push(`${count} ${label}${count === 1 ? '' : 's'} in Terminal`)
  }
  if (approvals > 0) parts.push(`${approvals} approval${approvals === 1 ? '' : 's'} in Approvals`)
  if (questions > 0) parts.push(`${questions} unanswered question${questions === 1 ? '' : 's'} in Run Room`)
  return {
    count: native + approvals + questions,
    questions,
    summary: parts.join('; '),
    destination: native === 0 && approvals > 0 ? 'approvals' : 'terminal',
  }
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

/**
 * The caller's own id and role in one object, which is what the permission
 * mirror in `lib/permissions.ts` asks for. Both are null before hydration.
 */
export function useSelf(): { id: string | null; role: Member['role'] | null } {
  const id = useStore((s) => s.info?.member.id ?? null)
  const role = useStore((s) => s.info?.member.role ?? null)
  return useMemo(() => ({ id, role }), [id, role])
}

/** Whether the caller holds the admin role. False until hydrated. */
export function useIsAdmin(): boolean {
  return useStore((s) => s.info?.member.role === 'admin')
}
