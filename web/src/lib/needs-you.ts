import { memberName as storeMemberName } from '@/lib/member-name'
import { needsInputPrefix, plainReason } from '@/lib/reasons'
import { isSudo } from '@/lib/run-requests'
import type {
  Device,
  Member,
  RoomMessage,
  RoomStatusResult,
  Run,
  ServerEdge,
  Workspace,
} from '@/lib/types'
import { queuedSteers, unansweredQuestions } from '@/store/collaboration'
import { isTerminal, type RunRecord } from '@/store/runs'

export interface StateContext {
  viewerID: string | null
  viewerRole: Member['role'] | null
  members: Record<string, Member>
  mergedInto: Record<string, string>
  runs: Record<string, RunRecord>
  workspaces: Record<string, Workspace>
  roomMessages: Record<string, RoomMessage[]>
  roomStatus: Record<string, RoomStatusResult | undefined>
  pausedRuns: Record<string, boolean>
  now: number
}

export type NeedsYouTarget = 'request' | 'run' | 'terminal' | 'changes' | 'notes'

export type NeedsYouID =
  | 'sudo'
  | 'permission'
  | 'question'
  | 'queued-message'
  | 'room-question'
  | 'disconnected'
  | 'blocked'
  | 'needs-input'
  | 'ready'
  | 'failed'

export interface PrimaryAction {
  kind: 'reply' | 'open'
  label: string
}

export const openAction: PrimaryAction = { kind: 'open', label: 'Open' }

/** How a row in Needs you is labelled: a request, or a run that needs intervention. */
export type AttentionState = 'needs-you' | 'ready' | 'failed' | 'disconnected'

export interface NeedsYouCondition {
  id: NeedsYouID
  state: AttentionState
  applies: (run: RunRecord, ctx: StateContext) => boolean
  waitsOn: (run: RunRecord, ctx: StateContext) => string | undefined
  reason: (run: RunRecord, ctx: StateContext) => string
  since: (run: RunRecord, ctx: StateContext) => string
  target: NeedsYouTarget
  action: (run: RunRecord) => PrimaryAction
}

interface Spec {
  id: NeedsYouID
  state?: AttentionState
  holds: (run: RunRecord, ctx: StateContext) => boolean
  resolvers: (run: RunRecord, ctx: StateContext) => (string | undefined)[]
  admins?: boolean
  background?: boolean
  /** Holds for a completed run too. */
  completed?: boolean
  reason: (run: RunRecord, ctx: StateContext) => string
  since?: (run: RunRecord, ctx: StateContext) => string | undefined
  target: NeedsYouTarget
  action: (run: RunRecord) => PrimaryAction
}

const isLive = (run: Pick<Run, 'status'>) => run.status === 'running' || run.status === 'needs-attention'

function condition(spec: Spec): NeedsYouCondition {
  const holds = (run: RunRecord, ctx: StateContext) =>
    ((isLive(run) && !isPaused(run, ctx)) || (spec.completed && run.status === 'completed')) &&
    (spec.background || run.mode !== 'headless') &&
    spec.holds(run, ctx)
  return {
    id: spec.id,
    state: spec.state ?? 'needs-you',
    target: spec.target,
    action: spec.action,
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

export const terminalAction: PrimaryAction = { kind: 'open', label: 'Open terminal' }

const answerIn = (run: RunRecord, enhancedLabel: string): PrimaryAction =>
  isEnhanced(run) ? { kind: 'open', label: enhancedLabel } : terminalAction

// `outcome_unseen` is owner-scoped on the server.
export function awaitingReview(run: Pick<Run, 'status' | 'outcome_unseen' | 'state'>): boolean {
  return run.outcome_unseen === true &&
    (run.status === 'completed' || run.status === 'failed' || run.state === 'finished' || run.state === 'failed')
}

/** A reported success the owner has not answered, or answered Not yet. */
export function awaitsCompletion(run: Pick<Run, 'status' | 'state' | 'review'>): boolean {
  return (run.review === 'ready' || run.review === 'later') &&
    (run.status === 'completed' || (isLive(run) && run.state === 'finished'))
}

export function isPaused(run: RunRecord, ctx: Pick<StateContext, 'pausedRuns'>): boolean {
  return !isTerminal(run.status) && (ctx.pausedRuns[run.id] ?? run.paused) === true
}

// Room status loads only while the run is open; an older gateway's snapshot has no controller.
function controller(run: RunRecord, ctx: StateContext): string | undefined {
  if (run.controller_member_id !== undefined) return run.controller_member_id || undefined
  return ctx.roomStatus[run.id]?.controller?.member_id
}

export function memberName(memberID: string, ctx: Pick<StateContext, 'members' | 'mergedInto'>): string {
  return storeMemberName(ctx, memberID)
}

const nativeRequests = (run: RunRecord, kinds: string[]) =>
  (run.pending_inputs ?? []).filter((request) => !isSudo(request) && kinds.includes(request.kind))

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

const blockedPrefix = 'blocked: '
// Mirrors usageLimitReason in internal/scheduler/usage_fallback.go.
const usageLimitPrefix = 'Usage limit reached on '

const asks = (run: RunRecord) => run.reason?.startsWith(needsInputPrefix) === true
const usageLimited = (run: RunRecord) => run.reason?.startsWith(usageLimitPrefix) === true
// Mirrors acpInterruptedReason and acpStoppedReason in internal/scheduler/acp_turn.go.
const turnCutShort = (run: RunRecord) => run.reason === 'turn interrupted' || run.reason?.startsWith('turn ended: ') === true
// Mirrors waitBrokenReasonPrefix in internal/scheduler/wait.go.
const waitBrokenPrefix = 'wait broken: '
const waitBroken = (run: RunRecord) => run.reason?.startsWith(waitBrokenPrefix) === true

// Checked in order; the specific requests precede the agent's own `needs-you`.
export const needsYouConditions: NeedsYouCondition[] = [
  condition({
    id: 'sudo',
    target: 'request',
    background: true,
    admins: true,
    holds: (run) => (run.pending_inputs ?? []).some(isSudo),
    resolvers: (run) => [run.member_id],
    reason: () => 'Permission requested',
    action: () => ({ kind: 'open', label: 'Answer' }),
  }),
  condition({
    id: 'permission',
    target: 'request',
    background: true,
    holds: (run) => nativeRequests(run, ['permission']).length > 0,
    resolvers: (run, ctx) => [run.member_id, controller(run, ctx)],
    reason: (run) => (isEnhanced(run) ? 'Permission requested' : 'Permission: answer in the terminal'),
    action: (run) => answerIn(run, 'Answer'),
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
    action: () => ({ kind: 'open', label: 'Review message' }),
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
    id: 'disconnected',
    state: 'disconnected',
    target: 'terminal',
    background: true,
    holds: (run) => run.state === 'disconnected',
    resolvers: (run) => [run.member_id],
    reason: (run) => (run.reason ? plainReason(run.reason) : 'Agent disconnected'),
    action: () => terminalAction,
  }),
  condition({
    id: 'blocked',
    target: 'terminal',
    background: true,
    holds: (run) => run.state === 'needs-you' && run.reason?.startsWith(blockedPrefix) === true,
    resolvers: (run) => [run.member_id],
    reason: (run) => `Blocked: ${run.reason?.slice(blockedPrefix.length)}`,
    action: () => terminalAction,
  }),
  condition({
    id: 'needs-input',
    target: 'run',
    background: true,
    // Any other needs-you is a request not loaded here yet, so it reads neutrally.
    holds: (run) => run.state === 'needs-you',
    resolvers: (run) => [run.member_id],
    reason: (run) => {
      if (turnCutShort(run)) return `${plainReason(run.reason!)} · reply to continue`
      if (waitBroken(run)) return `Wait broken: ${run.reason!.slice(waitBrokenPrefix.length)} · reply to continue`
      return asks(run) || usageLimited(run) ? plainReason(run.reason!) : 'Open the session to see its request'
    },
    // A Background run takes no input.
    action: (run) =>
      (asks(run) || turnCutShort(run) || waitBroken(run)) && run.mode !== 'headless' ? { kind: 'reply', label: 'Reply' } : openAction,
  }),
  condition({
    id: 'ready',
    state: 'ready',
    target: 'run',
    background: true,
    completed: true,
    // Leaves Needs you only on Mark complete or Not yet; opening the run does not answer it.
    holds: (run) => run.review === 'ready' && awaitsCompletion(run),
    resolvers: (run) => [run.member_id],
    reason: () => 'Your agent reports this task is finished',
    action: () => ({ kind: 'open', label: 'Review' }),
    since: (run) => run.review_changed_at,
  }),
  condition({
    id: 'failed',
    state: 'failed',
    target: 'changes',
    background: true,
    holds: (run) => run.state === 'failed' && run.outcome_unseen === true,
    resolvers: (run) => [run.member_id],
    action: () => ({ kind: 'open', label: 'Review' }),
    reason: (run) =>
      run.reason && run.reason !== 'agent reported failure'
        ? plainReason(run.reason)
        : 'Agent reported failure; review the result. Session remains open for follow-up.',
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

export interface DeviceNeed {
  device: Device
  reason: string
  admits: string
  waitingSince: string
}

/** Devices are not runs, so they sit beside the run table. A pending device
 * asks, and so does a registered one, admitted by signing in alone under
 * `account`, once the server is under `approved-devices`: it is refused
 * there until someone approves it. */
export function deviceNeeds(
  devices: Device[],
  viewer: Pick<StateContext, 'viewerID' | 'viewerRole'>,
  accessPolicy: ServerEdge['access_policy'] | undefined,
): DeviceNeed[] {
  const admin = viewer.viewerRole === 'admin'
  const waiting = (device: Device) =>
    admin
      ? device.status === 'pending' || (device.status === 'registered' && accessPolicy === 'approved-devices')
      // The server lists a non-admin's link devices (an invitation naming a member) only to that member.
      : device.status === 'pending' && (device.member_id === viewer.viewerID || (!device.member_id && Boolean(device.invitation_id && device.display_name)))
  return devices
    .filter(waiting)
    .map((device) => {
      const who = `${device.label} (${device.provider} ${device.account})`
      return {
        device,
        reason: !admin
          ? device.member_id ? `New device ${device.label} signed in as you` : `${who} is waiting on your account link`
          : device.status === 'registered'
          ? `${who} signed in as ${device.display_name} and is not approved`
          : device.display_name
            ? `${who} signed in as ${device.display_name}`
            : `${who} wants to join as ${device.role}`,
        admits: device.display_name ? `admits as: ${device.display_name} (${device.role})` : `new member, ${device.role}`,
        waitingSince: device.created_at,
      }
    })
    .sort((a, b) => a.waitingSince.localeCompare(b.waitingSince) || a.device.id.localeCompare(b.device.id))
}
