import type {
  Approval,
  Member,
  Mission,
  RoomMessage,
  RoomStatusResult,
  Run,
  Workspace,
} from '@/lib/types'
import { queuedSteers, unansweredQuestions } from '@/store/collaboration'
import type { MissionDetail } from '@/store/missions'
import { isTerminal, type RunRecord } from '@/store/runs'

export interface StateContext {
  viewerID: string | null
  viewerRole: Member['role'] | null
  members: Record<string, Member>
  runs: Record<string, RunRecord>
  workspaces: Record<string, Workspace>
  approvalsByRun: Record<string, Approval[]>
  roomMessages: Record<string, RoomMessage[]>
  roomStatus: Record<string, RoomStatusResult | undefined>
  missions: Record<string, Mission>
  missionDetails: Record<string, MissionDetail>
  pausedRuns: Record<string, boolean>
  now: number
}

export type NeedsYouTarget = 'request' | 'run' | 'changes' | 'notes' | 'swarm'

export type NeedsYouID =
  | 'permission'
  | 'question'
  | 'queued-message'
  | 'room-question'
  | 'swarm-question'
  | 'integrator-down'
  | 'control-hold'
  | 'enhanced-failure'
  | 'blocked'
  | 'stopped'
  | 'unreviewed-finish'

export interface PrimaryAction {
  kind: 'approve' | 'reply' | 'open'
  label: string
}

export const openAction: PrimaryAction = { kind: 'open', label: 'Open' }

export interface NeedsYouCondition {
  id: NeedsYouID
  applies: (run: RunRecord, ctx: StateContext) => boolean
  waitsOn: (run: RunRecord, ctx: StateContext) => string | undefined
  reason: (run: RunRecord, ctx: StateContext) => string
  since: (run: RunRecord, ctx: StateContext) => string
  target: NeedsYouTarget
  action: (run: RunRecord, approval: Approval | undefined) => PrimaryAction
}

interface Spec {
  id: NeedsYouID
  holds: (run: RunRecord, ctx: StateContext) => boolean
  resolvers: (run: RunRecord, ctx: StateContext) => (string | undefined)[]
  admins?: boolean
  background?: boolean
  supervised?: boolean
  reason: (run: RunRecord, ctx: StateContext) => string
  since?: (run: RunRecord, ctx: StateContext) => string | undefined
  target: NeedsYouTarget
  action?: (run: RunRecord, approval: Approval | undefined) => PrimaryAction
}

function condition(spec: Spec): NeedsYouCondition {
  const holds = (run: RunRecord, ctx: StateContext) =>
    (spec.background || run.mode !== 'headless') &&
    (spec.supervised || !supervised(run, ctx)) &&
    spec.holds(run, ctx)
  return {
    id: spec.id,
    target: spec.target,
    action: spec.action ?? (() => openAction),
    reason: spec.reason,
    since: (run, ctx) => spec.since?.(run, ctx) ?? run.stateChangedAt,
    applies: (run, ctx) => {
      if (!ctx.viewerID || !holds(run, ctx)) return false
      if (spec.admins && ctx.viewerRole === 'admin') return true
      return spec.resolvers(run, ctx).includes(ctx.viewerID)
    },
    waitsOn: (run, ctx) =>
      holds(run, ctx) ? spec.resolvers(run, ctx).find((id) => id) : undefined,
  }
}

export function isEnhanced(run: Pick<Run, 'acp'>): boolean {
  return run.acp === true
}

const answerIn = (run: RunRecord, enhancedLabel: string): PrimaryAction =>
  ({ kind: 'open', label: isEnhanced(run) ? enhancedLabel : 'Open terminal' })

// `outcome_unseen` is owner-scoped on the server.
export function awaitingReview(run: Pick<Run, 'status' | 'outcome_unseen'>): boolean {
  return run.outcome_unseen === true && (run.status === 'completed' || run.status === 'failed')
}

export function isPaused(run: RunRecord, ctx: Pick<StateContext, 'pausedRuns'>): boolean {
  return !isTerminal(run.status) && (ctx.pausedRuns[run.id] ?? run.paused) === true
}

function missionOf(run: RunRecord, ctx: StateContext): Mission | undefined {
  return run.mission_id ? ctx.missions[run.mission_id] : undefined
}

const swarmRunning = (mission: Mission | undefined) =>
  !mission || mission.phase === 'planning' || mission.phase === 'active'

function attemptHold(run: RunRecord, ctx: StateContext) {
  if (run.mission_role !== 'worker' || !run.mission_id) return undefined
  return ctx.missionDetails[run.mission_id]?.attempts.find(
    (attempt) => attempt.run_id === run.id && attempt.takeover_active,
  )
}

export function supervised(run: RunRecord, ctx: StateContext): boolean {
  if (run.mission_role !== 'worker' || !run.integrator_run_id) return false
  const integrator = ctx.runs[run.integrator_run_id]
  const mission = missionOf(run, ctx)
  return (
    integrator !== undefined &&
    !isTerminal(integrator.status) &&
    swarmRunning(mission) &&
    !mission?.integrator_launch_error &&
    attemptHold(run, ctx) === undefined
  )
}

// Room status loads only with the Run Room; an older gateway's snapshot has no controller.
function controller(run: RunRecord, ctx: StateContext): string | undefined {
  if (run.controller_member_id !== undefined) return run.controller_member_id || undefined
  return ctx.roomStatus[run.id]?.controller?.member_id
}

export function memberName(memberID: string, ctx: Pick<StateContext, 'members'>): string {
  return ctx.members[memberID]?.display_name ?? memberID
}

function waited(iso: string, now: number): string {
  const minutes = Math.floor((now - Date.parse(iso)) / 60_000)
  if (!Number.isFinite(minutes) || minutes < 1) return 'less than a minute'
  if (minutes < 60) return `${minutes} min`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} h`
  return `${Math.floor(hours / 24)} d`
}

const nativeRequests = (run: RunRecord, kinds: string[]) =>
  (run.pending_inputs ?? []).filter((request) => kinds.includes(request.kind))

const questionKinds = ['question', 'form', 'extension_ui']

function openRoomQuestions(run: RunRecord, ctx: StateContext): RoomMessage[] {
  return unansweredQuestions(ctx.roomMessages[run.id] ?? []).filter(
    (message) => message.actor_id !== run.member_id,
  )
}

function queuedMessages(run: RunRecord, ctx: StateContext): RoomMessage[] {
  const holder = controller(run, ctx)
  return queuedSteers(ctx.roomMessages[run.id] ?? []).filter(
    (message) => message.actor_id !== holder,
  )
}

function currentIntegrator(run: RunRecord, ctx: StateContext): Mission | undefined {
  const mission = missionOf(run, ctx)
  if (run.mission_role !== 'integrator' || !mission) return undefined
  if (mission.current_integrator_run_id !== run.id || !swarmRunning(mission)) return undefined
  return mission
}

function openSwarmQuestions(run: RunRecord, ctx: StateContext) {
  const mission = currentIntegrator(run, ctx)
  if (!mission) return []
  return (ctx.missionDetails[mission.id]?.questions ?? []).filter((q) => !q.answered_at)
}

function swarmHuman(run: RunRecord, ctx: StateContext): string | undefined {
  return (
    missionOf(run, ctx)?.accountable_human_id ??
    (run.integrator_run_id ? ctx.runs[run.integrator_run_id]?.member_id : undefined) ??
    run.member_id
  )
}

const blockedPrefix = 'blocked: '
// Mirrors the acp*Reason constants in internal/scheduler/acp_driver.go.
export const enhancedReasonPrefixes = [
  'enhanced session failed: ',
  'enhanced session ended: ',
  'enhanced turn failed: ',
] as const
const stalledPrefix = 'stalled:'

function enhancedFailure(run: RunRecord): string | undefined {
  const prefix = enhancedReasonPrefixes.find((p) => run.reason?.startsWith(p))
  return prefix === undefined ? undefined : run.reason!.slice(prefix.length)
}

// Order matters: the reason-prefix rows must precede `stopped`, which matches any parked run.
export const needsYouConditions: NeedsYouCondition[] = [
  condition({
    id: 'permission',
    target: 'request',
    background: true,
    holds: (run, ctx) =>
      nativeRequests(run, ['permission']).length > 0 || (ctx.approvalsByRun[run.id]?.length ?? 0) > 0,
    resolvers: (run, ctx) => [run.member_id, controller(run, ctx)],
    reason: (run, ctx) => {
      const approval = ctx.approvalsByRun[run.id]?.[0]
      if (approval) return `Permission: ${approval.action}`
      return isEnhanced(run) ? 'Permission requested' : 'Permission: answer in the terminal'
    },
    action: (run, approval) => (approval ? { kind: 'approve', label: 'Approve' } : answerIn(run, 'Open')),
    since: (run, ctx) => ctx.approvalsByRun[run.id]?.[0]?.created_at,
  }),
  condition({
    id: 'question',
    target: 'request',
    holds: (run) => nativeRequests(run, questionKinds).length > 0,
    resolvers: (run, ctx) => [run.member_id, controller(run, ctx)],
    reason: (run) => (isEnhanced(run) ? 'Question from the agent' : 'Question: answer in the terminal'),
    action: (run) => answerIn(run, 'Answer'),
  }),
  condition({
    id: 'queued-message',
    target: 'request',
    holds: (run, ctx) =>
      controller(run, ctx) !== undefined &&
      (queuedMessages(run, ctx).length > 0 ||
        (ctx.roomMessages[run.id] === undefined && (ctx.roomStatus[run.id]?.queued_steers ?? 0) > 0)),
    resolvers: (run, ctx) => [controller(run, ctx)],
    reason: (run, ctx) => {
      const message = queuedMessages(run, ctx)[0]
      const sender = message ? message.actor_display_name ?? memberName(message.actor_id, ctx) : 'A teammate'
      return `${sender} sent a message, approve to deliver`
    },
    since: (run, ctx) => queuedMessages(run, ctx)[0]?.created_at,
  }),
  condition({
    id: 'room-question',
    target: 'notes',
    // The snapshot count is authoritative; room history, loaded only with
    // the room and possibly stale, names the asker.
    holds: (run, ctx) =>
      run.unanswered_questions !== 0 &&
      (ctx.roomMessages[run.id] === undefined
        ? (run.unanswered_questions ?? 0) > 0
        : openRoomQuestions(run, ctx).length > 0),
    resolvers: (run) => [run.member_id],
    action: () => ({ kind: 'open', label: 'Answer' }),
    reason: (run, ctx) => {
      const question = openRoomQuestions(run, ctx)[0]
      if (!question) return 'A teammate asked a question'
      return `${question.actor_display_name ?? memberName(question.actor_id, ctx)} asked you: ${question.body}`
    },
    since: (run, ctx) => openRoomQuestions(run, ctx)[0]?.created_at,
  }),
  condition({
    id: 'swarm-question',
    target: 'swarm',
    background: true,
    holds: (run, ctx) =>
      openSwarmQuestions(run, ctx).length > 0 || (currentIntegrator(run, ctx)?.open_questions ?? 0) > 0,
    resolvers: (run, ctx) => [currentIntegrator(run, ctx)?.accountable_human_id],
    admins: true,
    action: () => ({ kind: 'open', label: 'Answer' }),
    reason: (run, ctx) => {
      const question = openSwarmQuestions(run, ctx)[0]
      return question ? `The integrator asks: ${question.body}` : 'The integrator has a question'
    },
    since: (run, ctx) => openSwarmQuestions(run, ctx)[0]?.asked_at,
  }),
  condition({
    id: 'integrator-down',
    target: 'swarm',
    background: true,
    holds: (run, ctx) => {
      const mission = currentIntegrator(run, ctx)
      return mission !== undefined && (Boolean(mission.integrator_launch_error) || isTerminal(run.status))
    },
    resolvers: (run, ctx) => [swarmHuman(run, ctx)],
    admins: true,
    reason: (run, ctx) =>
      missionOf(run, ctx)?.integrator_launch_error
        ? 'Integrator failed to launch, replace it to continue'
        : 'Integrator stopped, replace it to continue',
    since: (run, ctx) => missionOf(run, ctx)?.integrator_launch_error_at,
  }),
  condition({
    id: 'control-hold',
    target: 'run',
    background: true,
    supervised: true,
    holds: (run, ctx) => attemptHold(run, ctx)?.takeover_member_id !== undefined,
    resolvers: (run, ctx) => [attemptHold(run, ctx)?.takeover_member_id],
    reason: (run, ctx) => `You hold control of worker ${attemptHold(run, ctx)?.number ?? ''}`.trimEnd(),
  }),
  condition({
    id: 'enhanced-failure',
    target: 'run',
    background: true,
    holds: (run) => enhancedFailure(run) !== undefined,
    resolvers: (run) => [run.member_id],
    reason: (run) => `Enhanced unavailable: ${enhancedFailure(run)}`,
  }),
  condition({
    id: 'blocked',
    target: 'run',
    background: true,
    supervised: true,
    holds: (run) => run.status === 'needs-attention' && run.reason?.startsWith(blockedPrefix) === true,
    resolvers: (run, ctx) => [run.mission_role === 'worker' ? swarmHuman(run, ctx) : run.member_id],
    reason: (run) =>
      `${run.mission_role === 'worker' ? 'Worker blocked' : 'Blocked'}: ${run.reason?.slice(blockedPrefix.length)}`,
  }),
  condition({
    id: 'stopped',
    target: 'run',
    holds: (run, ctx) =>
      run.status === 'needs-attention' &&
      !isPaused(run, ctx) &&
      !run.reason?.startsWith(blockedPrefix) &&
      enhancedFailure(run) === undefined,
    resolvers: (run) => [run.member_id],
    action: () => ({ kind: 'reply', label: 'Reply' }),
    reason: (run, ctx) => {
      const label = run.reason?.startsWith(stalledPrefix) ? 'No activity' : 'Agent idle'
      return run.stateChangedAtEstimated ? label : `${label} for ${waited(run.stateChangedAt, ctx.now)}`
    },
  }),
  condition({
    id: 'unreviewed-finish',
    target: 'changes',
    background: true,
    holds: (run) => awaitingReview(run),
    resolvers: (run) => [run.member_id],
    action: () => ({ kind: 'open', label: 'Review' }),
    reason: (run) =>
      run.status === 'failed' ? 'Failed, review the result' : 'Finished, review the result',
  }),
]

export function needsYou(run: RunRecord, ctx: StateContext): NeedsYouCondition | undefined {
  return needsYouConditions.find((entry) => entry.applies(run, ctx))
}

export function waitingOn(run: RunRecord, ctx: StateContext): string | undefined {
  if (!ctx.viewerID) return undefined
  for (const entry of needsYouConditions) {
    const member = entry.waitsOn(run, ctx)
    if (member !== undefined) return member
  }
  return undefined
}
