import { useEffect, useMemo } from 'react'
import { useShallow } from 'zustand/react/shallow'
import { useClock } from '@/lib/clock'
import { deviceNeeds, type DeviceNeed, type StateContext } from '@/lib/needs-you'
import { isSudo } from '@/lib/run-requests'
import { presentRun, type PresentationState, type RunPresentation } from '@/lib/status'
import type { GatewayCapabilities, Member, Run, RunStatusCause, RunWait } from '@/lib/types'
import { unansweredQuestions } from '@/store/collaboration'
import { runPeople, runWatchers } from '@/store/presence'
import type { RunRecord } from '@/store/runs'
import { useStore } from '@/store'
import { runLabel } from '@/lib/status'
import { runFacts, type ChildRunFacts } from '@/store/workflow'
import {
  descendantRuns,
  listedRuns,
  needsYouByWorkspace,
  sidebarGroups,
  stateContextOf,
  type RunRow,
  type SidebarGroup,
} from '@/store/selectors'

/** The child runs below `rootIDs` as workflow rows. */
export function useChildRunFacts(rootIDs: string[]): ChildRunFacts[] {
  const now = useClock()
  const key = useStore((s) => {
    const ctx = stateContextOf(s, now)
    return JSON.stringify(descendantRuns(s.runs, rootIDs).map((run): ChildRunFacts => {
      const shown = presentRun(run, ctx)
      return { ...runFacts(run, shown, runLabel(run), shown.needsYou !== undefined), runId: run.id, parentRunId: run.parent_run_id! }
    }))
  })
  return useMemo(() => JSON.parse(key) as ChildRunFacts[], [key])
}

export function useStateContext(): StateContext {
  const now = useClock()
  const fields = useStore(useShallow((s) => stateContextOf(s, 0)))
  return useMemo(() => ({ ...fields, now }), [fields, now])
}

export function useDeviceNeeds(): DeviceNeed[] {
  const devices = useStore((s) => s.devices)
  const { id, role } = useSelf()
  const policy = useStore((s) => s.edge?.access_policy)
  return useMemo(() => deviceNeeds(devices, { viewerID: id, viewerRole: role }, policy), [devices, id, role, policy])
}

export function useSidebarGroups(): SidebarGroup[] {
  const ctx = useStateContext()
  const workspace = useStore((s) => s.activeWorkspace)
  const mineOnly = useStore((s) => s.mineOnly)
  const devices = useDeviceNeeds().length + useStore((s) => s.mergeProposals.filter((p) => p.minted_id === s.info?.member.id).length)
  return useMemo(() => sidebarGroups({ workspace, mineOnly, ctx }, devices), [workspace, mineOnly, ctx, devices])
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
    return JSON.stringify([shown.state, shown.reason, shown.next, shown.cause, shown.wait])
  })
  return useMemo(() => {
    const [state, reason, next, cause, wait] = JSON.parse(key) as [PresentationState, string, string | undefined, RunStatusCause | undefined, RunWait | undefined]
    return { state, reason, ...(next !== undefined && { next }), ...(cause !== undefined && { cause }), ...(wait && { wait }) }
  }, [key])
}

/** Which of these runs need the viewer; stable while that set is. */
export function useNeedsYouRuns(ids: string[]): ReadonlySet<string> {
  const now = useClock()
  const needy = useStore(
    useShallow((s) => {
      const ctx = stateContextOf(s, now)
      return ids.filter((id) => s.runs[id] && presentRun(s.runs[id], ctx).needsYou !== undefined)
    }),
  )
  return useMemo(() => new Set(needy), [needy])
}

/** One run's record; re-renders only when that run changes. */
export function useRun(runID: string): RunRecord | undefined {
  return useStore((s) => s.runs[runID])
}

/** Who is on a run, in the order `runPeople` gives; stable while that list is. */
export function useRunPeople(run: RunRecord): string[] {
  return useStore(useShallow((s) => runPeople(run, runWatchers(s.presence, run.id), s)))
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
  const questions = useStore((s) => run.unanswered_questions ??
    unansweredQuestions(s.roomMessages[run.id] ?? []).filter((m) => m.actor_id !== run.member_id).length)
  const requests = run.pending_inputs ?? []
  const native = requests.length
  const parts: string[] = []
  const sudo = requests.filter(isSudo).length
  if (sudo > 0) parts.push(`${sudo} sudo command${sudo === 1 ? '' : 's'} to allow`)
  for (const [kind, label] of [
    ['question', 'question'],
    ['permission', 'permission request'],
    ['form', 'form'],
    ['extension_ui', 'extension dialog'],
  ] as const) {
    const count = requests.filter((request) => !isSudo(request) && request.kind === kind).length
    if (count > 0) parts.push(`${count} ${label}${count === 1 ? '' : 's'} in Terminal`)
  }
  if (questions > 0) parts.push(`${questions} unanswered question${questions === 1 ? '' : 's'} in the session's notes`)
  return {
    count: native + questions,
    questions,
    summary: parts.join('; '),
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
  'presence.roster': true,
  'presence.heartbeat': true,
  'presence.typing': true,
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

/**
 * Whether a per-member read can be filed under the identity hydration sets;
 * one sent before it is repeated once it lands. An unlinked gateway hydrates
 * with no identity.
 */
export function useIdentityKnown(): boolean {
  return useStore((s) => s.identityKey !== null || s.hydrated)
}

/** Whether the caller holds the admin role. False until hydrated. */
export function useIsAdmin(): boolean {
  return useStore((s) => s.info?.member.role === 'admin')
}

/** While `shown`, the phone top bar shrinks New run to an icon, so the page's own action stays the one filled button. */
export function useHeaderPrimary(shown: boolean) {
  const hold = useStore((s) => s.holdHeaderPrimary)
  useEffect(() => (shown ? hold() : undefined), [shown, hold])
}
