import { useMemo } from 'react'
import { useShallow } from 'zustand/react/shallow'
import { useClock } from '@/lib/clock'
import type { StateContext } from '@/lib/needs-you'
import { presentRun, type PresentationState, type RunPresentation } from '@/lib/status'
import type { GatewayCapabilities, Member, Run } from '@/lib/types'
import { unansweredQuestions } from '@/store/collaboration'
import type { RunRecord } from '@/store/runs'
import { useStore } from '@/store'
import {
  listedRuns,
  needsYouByWorkspace,
  sidebarGroups,
  stateContextOf,
  type RunRow,
  type SidebarGroup,
} from '@/store/selectors'

export function useStateContext(): StateContext {
  const now = useClock()
  const fields = useStore(useShallow((s) => stateContextOf(s, 0)))
  return useMemo(() => ({ ...fields, now }), [fields, now])
}

export function useSidebarGroups(): SidebarGroup[] {
  const ctx = useStateContext()
  const workspace = useStore((s) => s.activeWorkspace)
  const mineOnly = useStore((s) => s.mineOnly)
  return useMemo(() => sidebarGroups({ workspace, mineOnly, ctx }), [workspace, mineOnly, ctx])
}

export function useNeedsYouCount(workspace?: string): number {
  const counts = useNeedsYouByWorkspace()
  return workspace === undefined
    ? Object.values(counts).reduce((total, n) => total + n, 0)
    : counts[workspace] ?? 0
}

export function useNeedsYouByWorkspace(): Record<string, number> {
  const ctx = useStateContext()
  return useMemo(() => needsYouByWorkspace(ctx), [ctx])
}

export function useRunPresentation(run: RunRecord): RunPresentation {
  const now = useClock()
  const key = useStore((s) => {
    const shown = presentRun(run, stateContextOf(s, now))
    return JSON.stringify([shown.state, shown.reason])
  })
  return useMemo(() => {
    const [state, reason] = JSON.parse(key) as [PresentationState, string]
    return { state, reason }
  }, [key])
}

/** One run's record; re-renders only when that run changes. */
export function useRun(runID: string): RunRecord | undefined {
  return useStore((s) => s.runs[runID])
}

/** The IDs of a workspace's runs, every run when it is empty; stable while that set is. */
export function useRunIDs(workspace: string): string[] {
  return useStore(
    useShallow((s) =>
      Object.values(s.runs)
        .filter((run) => !workspace || run.workspace_id === workspace)
        .map((run) => run.id),
    ),
  )
}

export function useRunInput(run: Run) {
  const approvals = useStore((s) => s.approvalsByRun[run.id]?.length ?? 0)
  const questions = useStore((s) => run.unanswered_questions ??
    unansweredQuestions(s.roomMessages[run.id] ?? []).filter((m) => m.actor_id !== run.member_id).length)
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
  if (questions > 0) parts.push(`${questions} unanswered question${questions === 1 ? '' : 's'} in the run's notes`)
  return {
    count: native + approvals + questions,
    questions,
    summary: parts.join('; '),
    destination: native === 0 && approvals > 0 ? 'approvals' : 'run',
  }
}

export function useListedRuns(workspace: string): RunRow[] {
  const ctx = useStateContext()
  return useMemo(() => listedRuns(workspace, ctx), [workspace, ctx])
}

export interface Capability {
  hasMethod: (method: string) => boolean
  hasLocal: (verb: string) => boolean
  hasWS: (name: string) => boolean
}

/** For a gateway whose /capabilities did not answer: the read-and-steer set
 * every gateway serves, so an unknown gateway degrades to monitoring. */
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

/** Null means a legacy remote monitor that predates /capabilities: the
 * fallback allowlist, both sockets, no local verbs. "*" means every method. */
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

/** Null before hydration. An admin affordance needs this as well as the
 * gateway capability: the local gateway advertises every method to anyone. */
export function useSelfRole(): Member['role'] | null {
  return useStore((s) => s.info?.member.role ?? null)
}

/** Both null before hydration. */
export function useSelf(): { id: string | null; role: Member['role'] | null } {
  const id = useStore((s) => s.info?.member.id ?? null)
  const role = useStore((s) => s.info?.member.role ?? null)
  return useMemo(() => ({ id, role }), [id, role])
}

/** Whether the caller holds the admin role. False until hydrated. */
export function useIsAdmin(): boolean {
  return useStore((s) => s.info?.member.role === 'admin')
}
