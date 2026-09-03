// Every verb the dashboard can perform on a run or on the board, as data.
// The command palette and the visible action buttons render the same list, so
// a label, an icon or a capability gate is written once and both surfaces
// agree. Nothing here touches the store's run state: the steering verbs go to
// the gateway and the event stream reports the result.

import type { LucideIcon } from 'lucide-react'
import {
  Archive,
  CheckCheck,
  Download,
  FileText,
  GitMerge,
  LayoutGrid,
  List,
  MessageSquarePlus,
  Pause,
  Play,
  RefreshCw,
  Rocket,
  Shield,
  ShieldOff,
  Square,
  UserPlus,
} from 'lucide-react'
import { useCallback } from 'react'
import { toast } from 'sonner'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import { allowed } from '@/lib/permissions'
import { runState } from '@/lib/status'
import type { Member, PullResult, RunStatus } from '@/lib/types'
import { useStore } from '@/store'
import type { Capability } from '@/store/hooks'
import type { PaletteDialog } from '@/store/palette'
import type { RunRecord } from '@/store/runs'

/** What a command needs to do its work, supplied by the surface running it. */
export interface CommandDeps {
  api: Api
  navigate: (name: string, params?: Record<string, string>) => void
  openDialog: (dialog: PaletteDialog, runID?: string) => void
  ackAll: () => void
  /** Keeps a pull's git output for the diff tab to show. */
  recordPull: (runID: string, result: PullResult) => void
  /** The template form's open state lives with the dialog host, not the store. */
  onTemplates: () => void
}

/** One thing a member can do, however the surface chooses to draw it. */
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
   * dialog openers report nothing.
   */
  done?: string
  /**
   * A success toast that names something the call returned - the ref a pull
   * fetched - instead of the flat past-tense one.
   */
  report?: (result: unknown) => string
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
   * refuse. See the paused-badge gap in docs/dashboard-frontend.md.
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
  /** The caller's own role, null before hydration. */
  role: Member['role'] | null
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
      label: 'Inject a message...',
      short: 'Inject',
      Icon: MessageSquarePlus,
      perform: (d) => d.openDialog('inject', id),
    })
  }

  if (!finished && mayKill) {
    list.push({
      id: 'close:merged',
      label: 'Close as merged',
      short: 'Merged',
      Icon: GitMerge,
      done: 'Closed as merged',
      confirm: {
        title: 'Close as merged?',
        body: 'The run is recorded as merged and leaves the board.',
        action: 'Close as merged',
      },
      perform: (d) => d.api.runClose(id, 'merged'),
    })
    list.push({
      id: 'close:abandoned',
      label: 'Close as abandoned',
      short: 'Abandoned',
      Icon: Archive,
      done: 'Closed as abandoned',
      confirm: {
        title: 'Close as abandoned?',
        body: 'The run is recorded as abandoned and leaves the board.',
        action: 'Close as abandoned',
      },
      perform: (d) => d.api.runClose(id, 'abandoned'),
    })
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
  if (finished && cap.hasMethod('run.relaunch') && maySteer) {
    list.push({
      id: 'relaunch',
      label: 'Relaunch run',
      short: 'Relaunch',
      Icon: RefreshCw,
      done: 'Relaunched',
      perform: (d) => d.api.runRelaunch(id),
    })
  }
  // Pull is the desktop gateway fetching into the repository on this machine,
  // not a call against the run, so it answers to the local capability alone.
  if (cap.hasLocal('pull')) {
    list.push({
      id: 'pull',
      label: 'Pull branch',
      short: 'Pull',
      Icon: Download,
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
export function canLaunch({ cap, role }: BoardCommandContext): boolean {
  return cap.hasMethod('run.launch') && allowed('launch', { id: null, role })
}

/** The verbs that act on the board rather than on one run. */
export function boardCommands(ctx: BoardCommandContext): Command[] {
  const list: Command[] = [
    {
      id: 'board',
      label: 'Open the run board',
      Icon: LayoutGrid,
      perform: (d) => d.navigate('board'),
    },
    {
      id: 'overview',
      label: 'Open every run as a list',
      Icon: List,
      perform: (d) => d.navigate('overview'),
    },
  ]
  if (canLaunch(ctx)) {
    list.push({
      id: 'launch',
      label: 'Launch a run...',
      Icon: Rocket,
      perform: (d) => d.openDialog('launch'),
    })
  }
  if (ctx.cap.hasMethod('template.launch') && allowed('launch', { id: null, role: ctx.role })) {
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
  const ackAll = useStore((s) => s.ackAll)
  const recordPull = useStore((s) => s.recordPull)
  const { onDone, onTemplates } = opts

  return useCallback(
    async (command: Command) => {
      onDone?.()
      const result = command.perform({
        api,
        navigate,
        openDialog,
        ackAll,
        recordPull,
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
    [ackAll, navigate, onDone, onTemplates, openDialog, recordPull],
  )
}
