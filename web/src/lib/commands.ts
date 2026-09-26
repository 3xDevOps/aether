// Every verb the dashboard can perform on a run or on the board, as data.
// The command palette and the visible action buttons render the same list, so
// a label, an icon or a capability gate is written once and both surfaces
// agree. Gateway verbs go through the API; deleting also removes the
// confirmed run from the local store.

import type { LucideIcon } from 'lucide-react'
import {
  Archive,
  ArchiveRestore,
  Cable,
  CheckCheck,
  CircleCheck,
  Download,
  FileText,
  LayoutGrid,
  List,
  MessageSquarePlus,
  Network,
  Pause,
  Play,
  RefreshCw,
  Rocket,
  Shield,
  ShieldOff,
  Square,
  Trash2,
  UserPlus,
} from 'lucide-react'
import { useCallback } from 'react'
import { toast } from 'sonner'
import { api, ApiError, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import { allowed } from '@/lib/permissions'
import { runState } from '@/lib/status'
import type { Member, PullResult, RunStatus, Workspace } from '@/lib/types'
import { useStore } from '@/store'
import type { Capability } from '@/store/hooks'
import type { PaletteDialog } from '@/store/palette'
import { isArchivable, type RunRecord } from '@/store/runs'

/** What a command needs to do its work, supplied by the surface running it. */
export interface CommandDeps {
  api: Api
  navigate: (name: string, params?: Record<string, string>) => void
  openDialog: (dialog: PaletteDialog, runID?: string) => void
  openForwardDialog: (target: string) => void
  openClearDoneDialog: (plan: ClearDonePlan) => void
  ackAll: () => void
  /** Keeps a pull's git output for the diff tab to show. */
  recordPull: (runID: string, result: PullResult) => void
  /** Removes a run after the server has deleted its durable record. */
  removeRun: (runID: string) => void
  /** The template form's open state lives with the dialog host, not the store. */
  onTemplates: () => void
}

export interface Command {
  id: string
  /** The full sentence, which is what the palette reads best. */
  label: string
  /**
   * The one or two words a button uses instead, because eight of these sit
   * in one header row. The button's tooltip carries the full label.
   */
  short?: string
  Icon: LucideIcon
  /** Extra words the palette's fuzzy match should see (handoff targets). */
  value?: string
  /**
   * The past-tense toast on success, and the prefix of the failure toast.
   * Present only when the command calls the gateway; navigation and the
   * dialog openers report nothing because the thing they opened is the feedback.
   */
  done?: string
  /**
   * A success toast that names something the call returned - the ref a pull
   * fetched - instead of the flat past-tense one.
   */
  report?: (result: unknown) => string
  /** A command can be shown but unavailable until its prerequisite exists. */
  disabled?: boolean
  /**
   * Set on the verbs a member cannot take back. Buttons ask before running;
   * the palette does not, because a palette item is already two deliberate
   * steps (open, type, select) away from an accident.
   */
  confirm?: { title: string; body: string; action: string }
  perform: (deps: CommandDeps) => Promise<unknown> | void
}

/** What the focused-run verbs are gated on. */
export interface RunCommandContext {
  run: RunRecord
  /**
   * Undefined means nobody knows this run's pause state: hydration seeds it
   * from the run list's `paused` wire field, but a legacy gateway sends none,
   * so there a reloaded tab knows no run's state until a pause or resume
   * event arrives. Offer neither verb rather than the one the server would
   * refuse. See "Reason and paused on the wire" in
   * docs/dashboard-frontend.md.
   */
  paused: boolean | undefined
  cap: Capability
  members: Record<string, Member>
  /** The caller, for the permission questions the server will ask again. */
  self: { id: string | null; role: Member['role'] | null }
  /** The run's workspace steer_others policy, when the workspace is known. */
  steerOthers?: string
}

/** What the board-wide verbs are gated on. */
export interface BoardCommandContext {
  cap: Capability
  /** The caller's own id and role, null before hydration. */
  self: { id: string | null; role: Member['role'] | null }
  /**
   * The Done column's live cards in the caller's scope, so Clear done can
   * weigh what it would archive.
   */
  doneCandidates: ClearDoneCandidate[]
}

/** One Done-column card, as much as Clear done needs to weigh it. */
export interface ClearDoneCandidate {
  run: RunRecord
  /** The run's workspace, for the steer_others policy the kill permission reads. */
  workspace?: Workspace
}

export interface ClearDonePlan {
  /** Runs Clear done would archive. */
  eligible: RunRecord[]
  /** Completed runs that have stopped but still await Close; archiving
   * cannot act on them until then. */
  notClosed: number
  /** Runs whose status qualifies but this member may not kill. */
  notAllowed: number
}

/**
 * What "Clear done" would archive out of the Done column's live cards:
 * every `isArchivable` run, not already archived, that this member may
 * kill, gated the same way the single Archive command is - see
 * "Archiving hides a finished run" in docs/dashboard-frontend.md. The rest
 * stay for one of two reasons, counted separately for the confirm dialog.
 */
export function clearDonePlan(
  candidates: ClearDoneCandidate[],
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

// internal/protocol.CodeNotFound: the run is already gone, which is the
// outcome Clear done was trying to reach anyway.
const codeRunNotFound = -32000

/**
 * Archives every eligible run at once; the server serializes the writes
 * itself. Each call's own `run.archived` event moves the run in every
 * connected dashboard, this one included, so the count below comes from the
 * settled calls, not from re-applying what the RPC returned.
 */
export async function runClearDone(
  eligible: RunRecord[],
  deps: Pick<CommandDeps, 'api' | 'removeRun'>,
): Promise<void> {
  const results = await Promise.allSettled(
    eligible.map((run) => deps.api.runArchive(run.id, true)),
  )
  let archived = 0
  let failed = 0
  let firstError: string | undefined
  results.forEach((result, i) => {
    if (result.status === 'fulfilled') {
      archived++
      return
    }
    const err: unknown = result.reason
    if (err instanceof ApiError && err.code === codeRunNotFound) {
      deps.removeRun(eligible[i].id)
      archived++
      return
    }
    failed++
    firstError ??= message(err)
  })
  if (failed > 0) {
    toast.error(`Archived ${archived}, ${failed} failed: ${firstError}`)
  } else {
    toast.success(`Archived ${archived} ${archived === 1 ? 'run' : 'runs'}`)
  }
}

/**
 * Who may be handed a run. Viewers cannot own one, so the server refuses a
 * handoff to one; do not offer what will be refused. A pending member has not
 * been approved yet, and the current owner is not a target.
 */
function handoffTargets(
  run: RunRecord,
  members: Record<string, Member>,
): Member[] {
  return Object.values(members).filter(
    (m) => m.id !== run.member_id && !m.pending && m.role !== 'viewer',
  )
}

/**
 * One handoff command per eligible member, or none at all when the caller may
 * not give this run away: the server allows a handoff only from the run's
 * owner or an admin.
 */
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

/**
 * Everything that acts on one run, in the order both surfaces show it. The
 * handoff entries come last; a surface that draws them as a menu of its own
 * calls `handoffCommands` instead.
 */
export function runCommands(ctx: RunCommandContext): Command[] {
  const { run, paused, cap, self, steerOthers } = ctx
  const id = run.id
  const finished = isFinished(run.status)
  const target = { owner: run.member_id, protected: run.protected, steerOthers }
  // The same three questions internal/permissions asks. A verb the server
  // would answer with a denial is not offered on either surface.
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
        perform: (d) => d.api.runPause(id),
      })
    }
    list.push({
      id: 'inject',
      label: 'Send a message to the agent...',
      short: 'Send',
      Icon: MessageSquarePlus,
      perform: (d) => d.openDialog('inject', id),
    })
    if (cap.hasLocal('forward.start')) {
      list.push({
        id: 'forward',
        label: 'Forward a port...',
        short: 'Forward',
        Icon: Cable,
        perform: (d) => d.openForwardDialog(`run:${id}`),
      })
    }
  }

  // Close resolves the outcome from any state that holds a record: a live
  // run is stopped first, a finished one re-labeled. The dialog asks
  // merged or abandoned; Delete stays safe at every stage.
  if (run.status !== 'queued' && mayKill) {
    list.push({
      id: 'close',
      label: 'Close run...',
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
      perform: (d) =>
        d.api.runDelete(id).then(() => {
          d.removeRun(id)
        }),
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
        perform: (d) => d.api.runArchive(id, false),
      })
    } else {
      list.push({
        id: 'archive',
        label: 'Archive run',
        short: 'Archive',
        Icon: Archive,
        done: 'Archived',
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
      perform: (d) => d.api.runProtect(id, !run.protected),
    })
  }
  if (
    run.mode === 'tui' &&
    runState(run.status) === 'done' &&
    run.reason === 'closed; retained container' &&
    cap.hasMethod('run.relaunch') &&
    maySteer
  ) {
    list.push({
      id: 'relaunch',
      label: 'Relaunch run',
      short: 'Relaunch',
      Icon: RefreshCw,
      done: 'Relaunched',
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

  return list
}

/**
 * Whether the run has stopped for good. A pending approval only ever reads as
 * needs-attention, so the presentation state decides this on status alone.
 */
function isFinished(status: RunStatus): boolean {
  const state = runState(status)
  return state === 'done' || state === 'failed'
}

/**
 * Whether this member may start a run. The gateway capability descriptor says
 * what the transport carries; the role says what this member may do, and the
 * local gateway advertises every method regardless of who is behind it.
 */
export function canLaunch({
  cap,
  role,
}: {
  cap: Capability
  role: Member['role'] | null
}): boolean {
  return cap.hasMethod('run.launch') && allowed('launch', { id: null, role })
}

/** The verbs that act on the board rather than on one run. */
export function boardCommands(ctx: BoardCommandContext): Command[] {
  const role = ctx.self.role
  const list: Command[] = [
    {
      id: 'board',
      label: 'Open the board',
      Icon: LayoutGrid,
      perform: (d) => d.navigate('board'),
    },
    {
      id: 'overview',
      label: 'Open all runs',
      Icon: List,
      perform: (d) => d.navigate('overview'),
    },
  ]
  if (canLaunch({ cap: ctx.cap, role })) {
    list.push({
      id: 'launch',
      label: 'Launch a run...',
      Icon: Rocket,
      perform: (d) => d.openDialog('launch'),
    })
  }
  if (ctx.cap.hasMethod('mission.create') && allowed('launch', { id: null, role })) {
    list.push({
      id: 'swarm',
      label: 'Create swarm...',
      Icon: Network,
      perform: (d) => d.openDialog('swarm'),
    })
  }
  if (ctx.cap.hasMethod('template.launch') && allowed('launch', { id: null, role })) {
    list.push({
      id: 'template',
      label: 'Launch from a template...',
      Icon: FileText,
      perform: (d) => d.onTemplates(),
    })
  }
  list.push({
    id: 'ack-all',
    label: 'Mark all runs seen',
    Icon: CheckCheck,
    perform: (d) => d.ackAll(),
  })
  const plan = clearDonePlan(ctx.doneCandidates, ctx.cap, ctx.self)
  if (plan.eligible.length > 0) {
    list.push({
      id: 'clear-done',
      label: 'Clear done runs',
      Icon: Archive,
      perform: (d) => d.openClearDoneDialog(plan),
    })
  }
  return list
}

/**
 * Runs a command and reports the outcome the same way on every surface: the
 * gateway verbs toast their past-tense name or the server's refusal verbatim,
 * and the rest (navigation, the two forms) report nothing because the thing
 * they opened is the feedback. `onDone` is what the surface does first - the
 * palette closes itself; a button bar has nothing to close.
 */
export function useCommandRunner(
  opts: { onDone?: () => void; onTemplates?: () => void } = {},
): (command: Command) => Promise<void> {
  const navigate = useStore((s) => s.navigate)
  const openDialog = useStore((s) => s.openPaletteDialog)
  const openForwardDialog = useStore((s) => s.openForwardDialog)
  const openClearDoneDialog = useStore((s) => s.openClearDoneDialog)
  const ackAll = useStore((s) => s.ackAll)
  const recordPull = useStore((s) => s.recordPull)
  const removeRun = useStore((s) => s.removeRun)
  const { onDone, onTemplates } = opts

  return useCallback(
    async (command: Command) => {
      onDone?.()
      const result = command.perform({
        api,
        navigate,
        openDialog,
        openForwardDialog,
        openClearDoneDialog,
        ackAll,
        recordPull,
        removeRun,
        onTemplates: onTemplates ?? (() => {}),
      })
      const done = command.done
      if (!done) return
      try {
        const value = await result
        toast.success(command.report ? command.report(value) : done)
      } catch (err) {
        toast.error(`${done} failed: ${message(err)}`)
      }
    },
    [
      ackAll,
      navigate,
      onDone,
      onTemplates,
      openDialog,
      openForwardDialog,
      openClearDoneDialog,
      recordPull,
      removeRun,
    ],
  )
}
