// Run and board verbs as data: the palette and the action buttons render the same list.

import { useCallback } from 'react'
import { toast } from 'sonner'
import {
  Archive,
  ArchiveRestore,
  Cable,
  CircleCheck,
  Download,
  FileText,
  House,
  List,
  type LucideIcon,
  MessageSquarePlus,
  Monitor,
  Moon,
  Network,
  PackageX,
  Pause,
  Play,
  RefreshCw,
  Rocket,
  Shield,
  ShieldOff,
  Square,
  Sun,
  Trash2,
  UserPlus,
} from '@/components/icons'
import { api, ApiError, type Api } from '@/lib/api'
import { errorSentence, message } from '@/lib/format'
import { allowed } from '@/lib/permissions'
import type { LinkStatus, Member, PullResult, Workspace } from '@/lib/types'
import { useStore } from '@/store'
import type { Capability } from '@/store/hooks'
import type { PaletteDialog } from '@/store/palette'
import { isArchivable, isTerminal, type RunRecord } from '@/store/runs'
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
  perform: (deps: CommandDeps) => Promise<unknown> | void
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

// internal/protocol.CodeNotFound: the run was already deleted, which is the
// outcome bulk archive was trying to reach anyway.
const codeRunNotFound = -32000

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
      label: 'Free container…',
      short: 'Free container',
      Icon: PackageX,
      done: 'Freed container',
      confirm: {
        title: 'Free this run’s container?',
        body: 'Its container is removed and the run cannot be reopened. The run and its history remain visible; this does not archive it.',
        action: 'Free container',
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
      ? run.status === 'merged' || run.status === 'abandoned' || run.status === 'completed'
      : finished && agentReportRetained.has(run.reason ?? '')) &&
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

  return list
}

/** An agent report finished a TUI run and kept its container to relaunch. */
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
      Icon: Network,
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
      const result = command.perform({
        api,
        navigate,
        openDialog,
        openForwardDialog,
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
