// Run and board verbs as data: the palette and the action buttons render the same list.

import { useCallback } from 'react'
import { toast } from 'sonner'
import {
  Archive,
  ArchiveRestore,
  Cable,
  Check,
  CircleCheck,
  Download,
  Hourglass,
  FileText,
  GitMerge,
  House,
  List,
  type LucideIcon,
  MessageSquarePlus,
  Monitor,
  Moon,
  Waypoints,
  PackageX,
  Pause,
  Play,
  RefreshCw,
  Rocket,
  Shield,
  ShieldOff,
  Sparkles,
  Square,
  Sun,
  Trash2,
  UserPlus,
} from '@/components/icons'
import { api, ApiError, codeInvalidState, codeNotFound, type Api } from '@/lib/api'
import { errorSentence, message } from '@/lib/format'
import { awaitsCompletion } from '@/lib/needs-you'
import { allowed } from '@/lib/permissions'
import type { LinkStatus, Member, PullResult, Run, Workspace } from '@/lib/types'
import { dismissOnIdentityChange } from '@/components/failure'
import { sameIdentity, useStore } from '@/store'
import type { Capability } from '@/store/hooks'
import { optimistic, optimisticDeadlineMs, refusedByServer, type Mutation, type Outcome } from '@/store/optimistic'
import type { PaletteDialog } from '@/store/palette'
import { isArchivable, isTerminal, toRecord, type PredictionMarker, type RunRecord } from '@/store/runs'
import type { Theme } from '@/store/ui'

export interface CommandDeps {
  api: Api
  navigate: (name: string, params?: Record<string, string>) => void
  openDialog: (dialog: PaletteDialog, runID?: string) => void
  openForwardDialog: (target: string) => void
  setTheme: (theme: Theme) => void
  /** Keeps a pull's git output for the Changes view to show. */
  recordPull: (runID: string, result: PullResult) => void
  /** Removes a run after the server has deleted its durable record. */
  removeRun: (runID: string) => void
  onTemplates: () => void
}

export interface Command {
  id: string
  label: string
  /** The button's label; its tooltip carries the full one. */
  short?: string
  Icon: LucideIcon
  /** Extra words the palette's fuzzy match should see (handoff targets). */
  value?: string
  /** Success toast and failure-toast prefix; set only on gateway calls. */
  done?: string
  /** A success toast naming something the call returned, such as a pull's ref. */
  report?: (result: unknown) => string
  disabled?: boolean
  /** Set on irreversible verbs; every surface confirms before running them. */
  confirm?: { title: string; body: string; action: string }
  /** The state the verb's success certainly sets; the runner shows it before the server answers. */
  predict?: () => Prediction
  /** The server answers only once the change is done, which can outlast the deadline, so the prediction waits for that answer. */
  slow?: boolean
  perform: (deps: CommandDeps) => Promise<unknown> | void
}

export interface Prediction {
  runID: string
  record?: Partial<RunRecord>
  /** Paused lives in `pausedRuns`, not on the record. */
  paused?: boolean
  /** Delete takes the run off screen; see `deletingRuns`. */
  removed?: boolean
}

export interface RunCommandContext {
  run: RunRecord
  /**
   * Undefined when unknown (a legacy gateway sends no `paused`): offer neither
   * verb. See "Reason and paused on the wire" in docs/dashboard-frontend.md.
   */
  paused: boolean | undefined
  cap: Capability
  members: Record<string, Member>
  /** The caller, for the permission questions the server will ask again. */
  self: { id: string | null; role: Member['role'] | null }
  steerOthers?: string
  /** A predicted verb is pending on the run; the caller subscribes to it. */
  predicting?: boolean
}

export interface BoardCommandContext {
  cap: Capability
  /** Null before hydration. */
  self: { id: string | null; role: Member['role'] | null }
}

export interface RunActionCandidate {
  run: RunRecord
  /** For the steer_others policy the kill permission reads. */
  workspace?: Workspace
}

export interface ClearDonePlan {
  eligible: RunRecord[]
  /** Stopped but not yet closed; archive cannot act on them. */
  notClosed: number
  /** Runs whose status qualifies but this member may not kill. */
  notAllowed: number
}

export interface ReleaseFinishedPlan {
  eligible: RunRecord[]
}

/** The existing status and reason pair is the wire evidence of retention. */
export function isRetainedRun(run: RunRecord): boolean {
  if (!isTerminal(run.status)) return false
  if (run.container_retained_until || run.cleanup_pending) return true
  switch (run.reason) {
    case 'closed; retained container':
      return run.status === 'merged' || run.status === 'abandoned'
    case 'worker finished; retained container':
      return true
    case 'agent reported success; retained container':
      return run.status === 'completed'
    case 'agent reported failure; retained container':
      return run.status === 'failed'
    default:
      return false
  }
}

/** Reopen is explicit and only available while an interactive runtime is retained. */
export function canReopenRun(run: Pick<Run, 'mode' | 'status' | 'reason'>): boolean {
  return (run.mode === 'tui' || run.mode === 'acp') &&
    (run.reason === 'closed; retained container'
      ? run.status === 'merged' || run.status === 'abandoned' || run.status === 'completed'
      : isTerminal(run.status) && agentReportRetained.has(run.reason ?? ''))
}

export function releaseFinishedPlan(
  candidates: RunActionCandidate[],
  cap: Capability,
  self: { id: string | null; role: Member['role'] | null },
): ReleaseFinishedPlan {
  const plan: ReleaseFinishedPlan = { eligible: [] }
  if (!cap.hasMethod('run.release')) return plan
  for (const { run, workspace } of candidates) {
    if (!isRetainedRun(run)) continue
    if (!allowed('kill', self, {
      owner: run.member_id, protected: run.protected, steerOthers: workspace?.steer_others,
    })) continue
    plan.eligible.push(run)
  }
  return plan
}

/** Gated the same way as the single Archive command. */
export function clearDonePlan(
  candidates: RunActionCandidate[],
  cap: Capability,
  self: { id: string | null; role: Member['role'] | null },
): ClearDonePlan {
  const plan: ClearDonePlan = { eligible: [], notClosed: 0, notAllowed: 0 }
  if (!cap.hasMethod('run.archive')) return plan
  for (const { run, workspace } of candidates) {
    if (run.archived_at) continue
    if (!isArchivable(run.status)) {
      if (run.status === 'completed') plan.notClosed++
      continue
    }
    const target = { owner: run.member_id, protected: run.protected, steerOthers: workspace?.steer_others }
    if (!allowed('kill', self, target)) {
      plan.notAllowed++
      continue
    }
    plan.eligible.push(run)
  }
  return plan
}

// Both bulk actions bound gateway requests rather than opening one per run.
const bulkRunConcurrency = 6

/** Each call's `run.archived` event moves the run in every dashboard, so only settled calls are counted here. */
export async function runClearDone(
  eligible: RunRecord[],
  deps: Pick<CommandDeps, 'api' | 'removeRun'>,
): Promise<void> {
  // One slot per run, so the failures read back in Done order however the
  // calls settle.
  const errors = new Array<string | undefined>(eligible.length)
  let next = 0
  const worker = async () => {
    while (next < eligible.length) {
      const i = next++
      try {
        await deps.api.runArchive(eligible[i].id, true)
      } catch (err) {
        // The run was already deleted, so there is nothing left to archive.
        if (err instanceof ApiError && err.code === codeNotFound) {
          deps.removeRun(eligible[i].id)
        } else {
          errors[i] = message(err)
        }
      }
    }
  }
  await Promise.all(
    Array.from({ length: Math.min(bulkRunConcurrency, eligible.length) }, worker),
  )
  const failures = errors.filter((error) => error !== undefined)
  const archived = eligible.length - failures.length
  if (failures.length > 0) {
    toast.error(`Archived ${archived}, ${failures.length} failed: ${failures[0]}`)
  } else {
    toast.success(`Archived ${archived} ${archived === 1 ? 'run' : 'runs'}`)
  }
}

/** Release is independent of archive and must not remove a run's history. */
export async function runReleaseFinished(
  eligible: RunRecord[],
  deps: Pick<CommandDeps, 'api'>,
): Promise<void> {
  const errors = new Array<string | undefined>(eligible.length)
  let next = 0
  const worker = async () => {
    while (next < eligible.length) {
      const i = next++
      try {
        await deps.api.runRelease(eligible[i].id)
      } catch (err) {
        errors[i] = message(err)
      }
    }
  }
  await Promise.all(
    Array.from({ length: Math.min(bulkRunConcurrency, eligible.length) }, worker),
  )
  const failures = errors.filter((error) => error !== undefined)
  const released = eligible.length - failures.length
  if (failures.length > 0) {
    toast.error(`Freed ${released}, ${failures.length} failed: ${failures[0]}`)
  } else {
    toast.success(`Freed the containers of ${released} ${released === 1 ? 'run' : 'runs'}`)
  }
}

/** The server refuses a handoff to a viewer, who cannot own a run. */
function handoffTargets(
  run: RunRecord,
  members: Record<string, Member>,
): Member[] {
  return Object.values(members).filter(
    (m) => m.id !== run.member_id && !m.pending && m.role !== 'viewer',
  )
}

export function handoffCommands({ run, members, self }: RunCommandContext): Command[] {
  if (!allowed('handoff', self, { owner: run.member_id })) return []
  return handoffTargets(run, members).map((m) => ({
    id: `handoff:${m.id}`,
    label: `Hand off to ${m.display_name}`,
    Icon: UserPlus,
    value: `hand off ${m.display_name} ${m.id}`,
    done: `Handed off to ${m.display_name}`,
    perform: (d: CommandDeps) => d.api.runHandoff(run.id, m.id),
  }))
}

/** In display order, handoffs last; a surface with its own handoff menu calls `handoffCommands`. */
export function runCommands(ctx: RunCommandContext): Command[] {
  const { run, paused, cap, self, steerOthers } = ctx
  const id = run.id
  const finished = isTerminal(run.status)
  const target = { owner: run.member_id, protected: run.protected, steerOthers }
  // The same questions internal/permissions asks, so no verb is offered that the server would deny.
  const maySteer = allowed('steer', self, target)
  const mayKill = allowed('kill', self, target)
  const mayProtect = allowed('protect', self, target)
  const list: Command[] = []

  if (!finished && maySteer) {
    if (paused === true) {
      list.push({
        id: 'resume',
        label: 'Resume run',
        short: 'Resume',
        Icon: Play,
        done: 'Resumed',
        predict: () => ({ runID: id, paused: false }),
        perform: (d) => d.api.runResume(id),
      })
    }
    if (paused === false) {
      list.push({
        id: 'pause',
        label: 'Pause run',
        short: 'Pause',
        Icon: Pause,
        done: 'Paused',
        predict: () => ({ runID: id, paused: true }),
        perform: (d) => d.api.runPause(id),
      })
    }
    list.push({
      id: 'inject',
      label: 'Send a message to the agent…',
      short: 'Message',
      Icon: MessageSquarePlus,
      perform: (d) => d.openDialog('inject', id),
    })
    if (cap.hasLocal('forward.start')) {
      list.push({
        id: 'forward',
        label: 'Forward a port…',
        short: 'Forward',
        Icon: Cable,
        perform: (d) => d.openForwardDialog(`run:${id}`),
      })
    }
  }

  if (cap.hasMethod('run.review') && mayKill && completable(run)) {
    const live = !finished
    list.push({
      id: 'complete',
      label: 'Mark complete',
      short: 'Mark complete',
      Icon: Check,
      done: 'Marked complete',
      predict: () => ({
        runID: id,
        record: live
          ? {
              review: 'accepted',
              status: 'completed',
              state: '',
              stateChangedAt: new Date().toISOString(),
              stateChangedAtEstimated: true,
              stateChangedAtPredicted: true,
            }
          : { review: 'accepted' },
      }),
      slow: live,
      perform: (d) => d.api.runReview(id, 'complete'),
    })
    if (run.review === 'ready') {
      list.push({
        id: 'not-yet',
        label: 'Not yet: keep the run open',
        short: 'Not yet',
        Icon: Hourglass,
        done: 'Kept open',
        predict: () => ({ runID: id, record: { review: 'later' } }),
        perform: (d) => d.api.runReview(id, 'later'),
      })
    }
  }

  // Close works from any state that holds a record: a live run is stopped first.
  if (run.status !== 'queued' && mayKill) {
    list.push({
      id: 'close',
      label: 'Close run…',
      short: 'Close',
      Icon: CircleCheck,
      perform: (d) => d.openDialog('close', id),
    })
  }

  if (!finished && run.status !== 'needs-attention' && mayKill) {
    list.push({
      id: 'kill',
      label: 'Kill run',
      short: 'Kill',
      Icon: Square,
      done: 'Killed',
      confirm: {
        title: 'Kill this run?',
        body: 'The agent stops immediately. Work already committed to the run branch stays.',
        action: 'Kill run',
      },
      predict: () => ({ runID: id, record: { status: 'abandoned', reason: 'killed', ...finishedNow() } }),
      slow: true,
      perform: (d) => d.api.runKill(id),
    })
  }

  if (mayKill) {
    list.push({
      id: 'delete',
      label: 'Delete run',
      short: 'Delete',
      Icon: Trash2,
      done: 'Deleted',
      confirm: {
        title: 'Delete this run?',
        body: 'The run, transcript, and coordination records will be removed from Aether.',
        action: 'Delete run',
      },
      // Over another verb's pending prediction it waits for the server instead.
      predict: ctx.predicting ? undefined : () => ({ runID: id, removed: true }),
      // A live run is stopped and its evidence purged before the server answers.
      slow: true,
      perform: (d) =>
        d.api.runDelete(id).catch((err: unknown) => {
          // A Retry after a lost answer finds the run already deleted.
          if (!(err instanceof ApiError && err.code === codeNotFound)) throw err
        }).then(() => {
          d.removeRun(id)
        }),
    })
  }

  if (cap.hasMethod('run.release') && mayKill && isRetainedRun(run)) {
    list.push({
      id: 'release',
      label: 'Free container…',
      short: 'Free container',
      Icon: PackageX,
      done: 'Freed container',
      confirm: {
        title: 'Free this run’s container?',
        body: 'Its compute is released and the exact process cannot be reopened. Files, result branches and history have separate lifetimes; this does not delete all run data or archive it.',
        action: 'Free container',
      },
      predict: () => ({ runID: id, record: { container_retained_until: undefined, cleanup_pending: undefined } }),
      slow: true,
      perform: (d) => d.api.runRelease(id),
    })
  }

  if (cap.hasMethod('run.archive') && mayKill && isArchivable(run.status)) {
    if (run.archived_at) {
      list.push({
        id: 'restore',
        label: 'Restore run',
        short: 'Restore',
        Icon: ArchiveRestore,
        done: 'Restored',
        predict: () => ({ runID: id, record: { archived_at: undefined } }),
        perform: (d) => d.api.runArchive(id, false),
      })
    } else {
      list.push({
        id: 'archive',
        label: 'Archive run',
        short: 'Archive',
        Icon: Archive,
        done: 'Archived',
        predict: () => ({ runID: id, record: { archived_at: new Date().toISOString() } }),
        perform: (d) => d.api.runArchive(id, true),
      })
    }
  }

  if (cap.hasMethod('run.protect') && mayProtect) {
    list.push({
      id: 'protect',
      label: run.protected ? 'Unprotect run' : 'Protect run',
      short: run.protected ? 'Unprotect' : 'Protect',
      Icon: run.protected ? ShieldOff : Shield,
      done: run.protected ? 'Unprotected' : 'Protected',
      predict: () => ({ runID: id, record: { protected: !run.protected } }),
      perform: (d) => d.api.runProtect(id, !run.protected),
    })
  }
  if (
    canReopenRun(run) &&
    cap.hasMethod('run.relaunch') &&
    maySteer
  ) {
    list.push({
      id: 'relaunch',
      label: 'Reopen run',
      short: 'Reopen',
      Icon: RefreshCw,
      done: 'Reopened',
      perform: (d) => d.api.runRelaunch(id),
    })
  }
  if (cap.hasLocal('pull')) {
    list.push({
      id: 'pull',
      label: 'Pull branch',
      short: 'Pull',
      Icon: Download,
      disabled: !run.last_commit,
      done: 'Pulled branch',
      report: (result) => `Pulled ${(result as PullResult).ref}`,
      perform: (d) =>
        d.api.localPull(id).then((result) => {
          d.recordPull(id, result)
          return result
        }),
    })
  }

  // A verb offered from a pending prediction's state could re-read the run and erase that prediction.
  return ctx.predicting ? list.filter((command) => !command.predict) : list
}

/** A terminal status has no agent state; the server's run.status event replaces the guessed time. */
function finishedNow(): Partial<RunRecord> {
  return { state: '', stateChangedAt: new Date().toISOString(), stateChangedAtEstimated: true, stateChangedAtPredicted: true }
}

/** The Close dialog's choice; the server answers a repeat of a landed close as success. */
export function closeCommand(runID: string, outcome: 'merged' | 'abandoned'): Command {
  return {
    id: `close-${outcome}`,
    label: outcome === 'merged' ? 'Merged' : 'Abandoned',
    Icon: outcome === 'merged' ? GitMerge : Archive,
    done: `Closed as ${outcome}`,
    predict: () => ({ runID, record: { status: outcome, ...finishedNow() } }),
    // A live run is detached, committed and published before the server answers.
    slow: true,
    perform: (d) => d.api.runClose(runID, outcome),
  }
}

/** A run whose agent reported success and that its owner has not marked complete. */
export function completable(run: RunRecord): boolean {
  return awaitsCompletion(run) || ((run.status === 'running' || run.status === 'needs-attention') && run.state === 'finished' && run.review !== 'accepted')
}

/** Older agent-report finishes can still have a retained interactive runtime. */
const agentReportRetained = new Set([
  'agent reported success; retained container',
  'agent reported failure; retained container',
])

/** The local gateway advertises every method regardless of caller, so the role is checked too. */
export function canLaunch({
  cap,
  role,
}: {
  cap: Capability
  role: Member['role'] | null
}): boolean {
  return cap.hasMethod('run.launch') && allowed('launch', { id: null, role })
}

export const unlinkedReason = 'Link this computer to a server first'

/** An unlinked gateway serves only onboarding, so nothing can launch yet. */
export function launchBlocked(linkStatus: Pick<LinkStatus, 'server_configured'> | null): string | undefined {
  return linkStatus?.server_configured === false ? unlinkedReason : undefined
}

export function boardCommands(ctx: BoardCommandContext): Command[] {
  const role = ctx.self.role
  const list: Command[] = [
    {
      id: 'board',
      label: 'Open the board',
      Icon: House,
      perform: (d) => d.navigate('board'),
    },
    {
      id: 'overview',
      label: 'Open all workspaces',
      Icon: List,
      perform: (d) => d.navigate('overview'),
    },
  ]
  if (ctx.cap.hasMethod('assistant.status') && canLaunch({ cap: ctx.cap, role })) {
    list.push({
      id: 'assistant',
      label: 'Ask Aether',
      Icon: Sparkles,
      value: 'assistant ask question help',
      perform: () => useStore.getState().setAssistantOpen(true),
    })
  }
  if (canLaunch({ cap: ctx.cap, role })) {
    list.push({
      id: 'launch',
      label: 'New run…',
      Icon: Rocket,
      perform: (d) => d.openDialog('launch'),
    })
  }
  if (ctx.cap.hasMethod('mission.create') && allowed('launch', { id: null, role })) {
    list.push({
      id: 'swarm',
      label: 'New swarm…',
      Icon: Waypoints,
      perform: (d) => d.openDialog('swarm'),
    })
  }
  if (ctx.cap.hasMethod('template.launch') && allowed('launch', { id: null, role })) {
    list.push({
      id: 'template',
      label: 'Launch from a template…',
      Icon: FileText,
      perform: (d) => d.onTemplates(),
    })
  }
  // The confirmation computes which runs qualify when it opens.
  if (ctx.cap.hasMethod('run.archive')) {
    list.push({
      id: 'clear-done',
      label: 'Archive closed runs…',
      Icon: Archive,
      perform: (d) => d.openDialog('clear-done'),
    })
  }
  if (ctx.cap.hasMethod('run.release')) {
    list.push({
      id: 'release-finished',
      label: 'Free retained containers…',
      Icon: PackageX,
      perform: (d) => d.openDialog('release-finished'),
    })
  }
  list.push(
    {
      id: 'theme-system',
      label: 'Use system theme',
      Icon: Monitor,
      perform: (d) => d.setTheme('system'),
    },
    {
      id: 'theme-light',
      label: 'Use light theme',
      Icon: Sun,
      perform: (d) => d.setTheme('light'),
    },
    {
      id: 'theme-dark',
      label: 'Use dark theme',
      Icon: Moon,
      perform: (d) => d.setTheme('dark'),
    },
  )
  return list
}

/** Instants the client predicts with its own clock; a server record matches by presence. */
const instants = new Set(['archived_at'])
/** The client's guess at the change time, which the server's record replaces. */
const guessed = new Set(['stateChangedAt', 'stateChangedAtEstimated', 'stateChangedAtPredicted'])
/** The wire omits a field at its zero value (`omitempty`), so a re-read lacks a false or empty field. */
const omitZero = (value: unknown) => value || undefined

function holds({ runID, record, paused, removed }: Prediction): boolean {
  const s = useStore.getState()
  const run = s.runs[runID]
  return (
    (!record || (run !== undefined && Object.entries(record).every(([key, value]) => run[key as keyof RunRecord] === value))) &&
    (paused === undefined || s.pausedRuns[runID] === paused) &&
    (removed === undefined || removed === !run)
  )
}

function show({ runID, record, paused, removed }: Prediction) {
  const s = useStore.getState()
  if (record) s.patchRun(runID, record)
  if (paused !== undefined) s.setPaused(runID, paused)
  if (removed !== undefined) s.setDeleting(runID, removed)
}

/** Puts back each predicted value still on screen; a value anything else wrote stays. */
function unshow({ runID, record, paused, removed }: Prediction, before: Prediction) {
  const s = useStore.getState()
  const run = s.runs[runID]
  show({
    runID,
    record: record && run && Object.fromEntries(
      Object.keys(record)
        .filter((key) => run[key as keyof RunRecord] === record[key as keyof RunRecord])
        .map((key) => [key, before.record?.[key as keyof RunRecord]]),
    ),
    paused: paused !== undefined && s.pausedRuns[runID] === paused ? before.paused : undefined,
    removed: removed && s.deletingRuns[runID] ? before.removed : undefined,
  })
}

/**
 * Shows a `run.get` answer and says whether it shows the predicted state. A run
 * changed since the read was sent keeps its newer fields: the read only replaces
 * the predicted values still on screen.
 */
function applyRead(prediction: Prediction, run: Run, unchanged: boolean): boolean {
  const s = useStore.getState()
  if (prediction.removed) {
    // The server still holds the run; a removal that landed meanwhile is newer than the read.
    if (s.deletingRuns[run.id]) {
      s.setDeleting(run.id, false)
      s.upsertRun(run)
    }
    return false
  }
  const read = toRecord(run, s.runs[run.id])
  const server: Prediction = {
    runID: run.id,
    record: prediction.record && Object.fromEntries(Object.keys(prediction.record).map((key) => [key, read[key as keyof RunRecord]])),
    paused: run.paused,
  }
  if (unchanged) {
    s.upsertRun(run)
    if (run.paused !== undefined) s.setPaused(run.id, run.paused)
  } else unshow(prediction, server)
  const same = (key: string, value: unknown) => {
    const held = server.record?.[key as keyof RunRecord]
    return guessed.has(key) || (instants.has(key) ? Boolean(held) === Boolean(value) : omitZero(held) === omitZero(value))
  }
  return (
    (!prediction.record || Object.entries(prediction.record).every(([key, value]) => same(key, value))) &&
    (prediction.paused === undefined || (run.paused ?? prediction.paused) === prediction.paused)
  )
}

/** Whether the run's record and pause state are still those at this call; a `run.get` sent here is older than any change since. */
function unchangedSince(runID: string): () => boolean {
  const { runs, pausedRuns } = useStore.getState()
  const record = runs[runID]
  const paused = pausedRuns[runID]
  return () => {
    const s = useStore.getState()
    return s.runs[runID] === record && s.pausedRuns[runID] === paused
  }
}

function readRun(client: Api, runID: string): Promise<Run> {
  let timer: ReturnType<typeof setTimeout> | undefined
  return Promise.race([
    client.runGet(runID),
    new Promise<never>((_, reject) => (timer = setTimeout(reject, optimisticDeadlineMs))),
  ]).finally(() => clearTimeout(timer))
}

/** One press of a predicted verb, across its retries. */
interface Press {
  /** Set by any attempt's success, so no attempt reverts it or reports it failed afterwards. */
  confirmed: boolean
  /** The timeout toast a late success dismisses. */
  toast?: string | number
  /** From `sameIdentity()` at the press: once false, nothing of the press runs or shows. */
  same: () => boolean
}

/** `quiet`: nothing is left to report, or a late refusal reports itself. */
type Landing = 'shown' | 'quiet' | 'lost'

interface Attempt {
  outcome: Promise<Outcome<unknown>>
  landed: Promise<Landing>
  /** A slow verb's failure with no answer after the deadline. */
  lostError: () => string | undefined
}

/** An invalid-state refusal can follow a call that already landed, so it re-reads the run. */
function predicted(
  prediction: Prediction,
  send: () => Promise<unknown>,
  client: Api,
  press: Press,
  slow?: boolean,
): Mutation<unknown> & { landed: Promise<Landing>; lostError: () => string | undefined } {
  const { runID } = prediction
  let before = prediction
  let reverted = false
  let refused = false
  let lost = false
  let lostError: string | undefined
  let land = (_: Landing) => {}
  const landed = new Promise<Landing>((resolve) => (land = resolve))
  const marker: PredictionMarker = {}
  const finish = () => useStore.getState().setPredicting(runID, marker, false)
  const putBack = () => {
    if (press.confirmed || !press.same()) return
    unshow(prediction, before)
    reverted = true
  }
  const revert = () => {
    if (!press.same()) {
      finish()
      return land('quiet')
    }
    if (refused) return
    if (slow && !lost) {
      marker.waiting = true
      useStore.getState().setPredicting(runID, marker, true)
      return
    }
    const unchanged = unchangedSince(runID)
    void readRun(client, runID)
      .then((run) => {
        if (press.confirmed || !press.same()) return
        if (applyRead(prediction, run, unchanged())) press.confirmed = true
        else reverted = true
      }, (cause: unknown) => {
        // The server no longer holds a run whose delete landed.
        if (prediction.removed && cause instanceof ApiError && cause.code === codeNotFound && !press.confirmed && press.same()) {
          useStore.getState().removeRun(runID)
          press.confirmed = true
        } else putBack()
      })
      .finally(() => {
        finish()
        land(press.confirmed ? 'shown' : 'lost')
      })
  }
  return {
    landed,
    lostError: () => lostError,
    apply: () => {
      const s = useStore.getState()
      const run = s.runs[runID]
      before = {
        runID,
        record: prediction.record && run && Object.fromEntries(Object.keys(prediction.record).map((key) => [key, run[key as keyof RunRecord]])),
        paused: prediction.paused === undefined ? undefined : s.pausedRuns[runID],
        removed: prediction.removed === undefined ? undefined : !run,
      }
      show(prediction)
      s.setPredicting(runID, marker, true)
    },
    send: () =>
      send().catch(async (cause: unknown) => {
        if (!refusedByServer(cause)) {
          lost = true
          if (marker.waiting) {
            lostError = errorSentence(cause)
            revert()
          }
          throw cause
        }
        refused = true
        const unchanged = unchangedSince(runID)
        const run = cause instanceof ApiError && cause.code === codeInvalidState
          ? await readRun(client, runID).catch(() => undefined)
          : undefined
        if (run && !press.confirmed && press.same()) {
          if (applyRead(prediction, run, unchanged())) return
        } else putBack()
        finish()
        land('quiet')
        throw cause
      }),
    confirm: () => {
      if (!press.same()) {
        finish()
        return land('quiet')
      }
      press.confirmed = true
      finish()
      if (reverted && holds(before)) show(prediction)
      land(marker.waiting ? 'quiet' : 'shown')
    },
    revert,
  }
}

async function settle(done: string, runID: string, attempt: () => Attempt, press: Press): Promise<void> {
  const { outcome: pending, landed, lostError } = attempt()
  const outcome = await pending
  if (!press.same()) return
  if (outcome.ok) {
    toast.success(done)
    return
  }
  if (!outcome.unanswered) {
    toast.error(`${done} failed: ${outcome.error}`)
    return
  }
  const landing = await landed
  if (!press.same()) return
  if (landing === 'shown') {
    toast.success(done)
    return
  }
  const id = landing === 'lost'
    ? toast.error(`${done} failed: ${lostError() ?? outcome.error}`, {
      duration: Infinity,
      action: {
        label: 'Retry',
        onClick: (event) => {
          if (!press.same()) return
          if (!useStore.getState().predicting[runID]) return void settle(done, runID, attempt, press)
          event.preventDefault()
          toast.error(`${done} not retried: another action on this run is still waiting for the server.`)
        },
      },
    })
    : undefined
  if (id !== undefined) dismissOnIdentityChange(id, press.same)
  press.toast = id
  void outcome.refused?.then((error) => {
    if (!press.confirmed && press.same()) toast.error(`${done} failed: ${error}`, { id, action: undefined, duration: undefined })
  })
}

/** Gateway verbs toast their past-tense name or the server's refusal verbatim; dialog openers report nothing. */
export function useCommandRunner(
  opts: { onDone?: () => void; onTemplates?: () => void } = {},
): (command: Command) => Promise<void> {
  const navigate = useStore((s) => s.navigate)
  const openDialog = useStore((s) => s.openPaletteDialog)
  const openForwardDialog = useStore((s) => s.openForwardDialog)
  const recordPull = useStore((s) => s.recordPull)
  const removeRun = useStore((s) => s.removeRun)
  const setTheme = useStore((s) => s.setTheme)
  const { onDone, onTemplates } = opts

  return useCallback(
    async (command: Command) => {
      onDone?.()
      const deps: CommandDeps = {
        api,
        navigate,
        openDialog,
        openForwardDialog,
        recordPull,
        removeRun,
        setTheme,
        onTemplates: onTemplates ?? (() => {}),
      }
      const done = command.done
      if (command.predict && done) {
        const press: Press = { confirmed: false, same: sameIdentity() }
        const prediction = command.predict()
        const attempt = (): Attempt => {
          const mutation = predicted(prediction, async () => command.perform(deps), api, press, command.slow)
          const confirm: typeof mutation.confirm = (result) => {
            mutation.confirm(result)
            if (press.toast !== undefined) toast.dismiss(press.toast)
          }
          return { outcome: optimistic({ ...mutation, confirm }), landed: mutation.landed, lostError: mutation.lostError }
        }
        await settle(done, prediction.runID, attempt, press)
        return
      }
      const result = command.perform(deps)
      if (!done) return
      try {
        const value = await result
        toast.success(command.report ? command.report(value) : done)
      } catch (err) {
        toast.error(`${done} failed: ${errorSentence(err)}`)
      }
    },
    [
      navigate,
      onDone,
      onTemplates,
      openDialog,
      openForwardDialog,
      recordPull,
      removeRun,
      setTheme,
    ],
  )
}
