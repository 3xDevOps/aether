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
  House,
  List,
  MessageSquarePlus,
  Monitor,
  Moon,
  Network,
  Pause,
  Play,
  PackageX,
  RefreshCw,
  Rocket,
  Shield,
  ShieldOff,
  Square,
  Sun,
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
import type { Theme } from '@/store/ui'

/** What a command needs to do its work, supplied by the surface running it. */
export interface CommandDeps {
  api: Api
  navigate: (name: string, params?: Record<string, string>) => void
  openDialog: (dialog: PaletteDialog, runID?: string) => void
  openForwardDialog: (target: string) => void
  ackAll: () => void
  setTheme: (theme: Theme) => void
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
   * Set on the verbs a member cannot take back. Both action buttons and the
   * command palette ask for explicit confirmation before running them.
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
}

/** The run and workspace policy needed for Kill-gated bulk actions. */
export interface RunActionCandidate {
  run: RunRecord
  /** The run's workspace, for the steer_others policy the kill permission reads. */
  workspace?: Workspace
}

export interface ClearDonePlan {
  /** Runs Archive closed runs would archive. */
  eligible: RunRecord[]
  /** Completed runs that have stopped but still await Close; archiving
   * cannot act on them until then. */
  notClosed: number
  /** Runs whose status qualifies but this member may not kill. */
  notAllowed: number
}

export interface ReleaseFinishedPlan {
  eligible: RunRecord[]
}

/** The existing status and reason pair is the wire evidence of retention. */
export function isRetainedRun(run: RunRecord): boolean {
  if (!isFinished(run.status)) return false
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

/**
 * What Archive closed runs would archive out of the Done column's live cards:
 * every `isArchivable` run, not already archived, that this member may
 * kill, gated the same way as the single Archive command. Other candidates
 * stay for one of two reasons, counted separately for the confirm dialog.
 */
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

// internal/protocol.CodeNotFound: the run was already deleted, which is the
// outcome bulk archive was trying to reach anyway.
const codeRunNotFound = -32000

// Both bulk actions bound gateway requests rather than opening one per run.
const bulkRunConcurrency = 6

/**
 * Archives every eligible run, `bulkRunConcurrency` calls at a time. Each
 * call's own `run.archived` event moves the run in every connected
 * dashboard, this one included, so the count below comes from the settled
 * calls, not from re-applying what the RPC returned.
 */
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
        if (err instanceof ApiError && err.code === codeRunNotFound) {
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
    toast.error(`Released ${released}, ${failures.length} failed: ${failures[0]}`)
  } else {
    toast.success(`Released resources for ${released} ${released === 1 ? 'run' : 'runs'}`)
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
      short: 'Message',
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

  if (cap.hasMethod('run.release') && mayKill && isRetainedRun(run)) {
    list.push({
      id: 'release',
      label: 'Release resources...',
      short: 'Release',
      Icon: PackageX,
      done: 'Released resources',
      confirm: {
        title: 'Release this run’s resources?',
        body: 'Its container is removed and cannot be relaunched. The run and its history remain visible; this does not archive it.',
        action: 'Release resources',
      },
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
    (run.reason === 'closed; retained container'
      ? runState(run.status) === 'done'
      : finished && agentReportRetained.has(run.reason ?? '')) &&
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

/** An agent report finished a TUI run and kept its container to relaunch. */
const agentReportRetained = new Set([
  'agent reported success; retained container',
  'agent reported failure; retained container',
])

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
      Icon: House,
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
  // The confirmation computes which runs qualify when it opens, and says so
  // when none do; the palette does not build the board to find out first.
  if (ctx.cap.hasMethod('run.archive')) {
    list.push({
      id: 'clear-done',
      label: 'Archive closed runs...',
      Icon: Archive,
      perform: (d) => d.openDialog('clear-done'),
    })
  }
  if (ctx.cap.hasMethod('run.release')) {
    list.push({
      id: 'release-finished',
      label: 'Release finished resources...',
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

/**
 * Runs a command and reports the outcome the same way on every surface: the
 * gateway verbs toast their past-tense name or the server's refusal verbatim,
 * and dialog openers report nothing because the surface they open provides
 * feedback. `onDone` closes the palette before running a command; action
 * buttons have nothing to close.
 */
export function useCommandRunner(
  opts: { onDone?: () => void; onTemplates?: () => void } = {},
): (command: Command) => Promise<void> {
  const navigate = useStore((s) => s.navigate)
  const openDialog = useStore((s) => s.openPaletteDialog)
  const openForwardDialog = useStore((s) => s.openForwardDialog)
  const ackAll = useStore((s) => s.ackAll)
  const recordPull = useStore((s) => s.recordPull)
  const removeRun = useStore((s) => s.removeRun)
  const setTheme = useStore((s) => s.setTheme)
  const { onDone, onTemplates } = opts

  return useCallback(
    async (command: Command) => {
      onDone?.()
      const result = command.perform({
        api,
        navigate,
        openDialog,
        openForwardDialog,
        ackAll,
        recordPull,
        removeRun,
        setTheme,
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
      recordPull,
      removeRun,
      setTheme,
    ],
  )
}
