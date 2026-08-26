import {
  Archive,
  Bot,
  CheckCheck,
  Compass,
  Download,
  Eye,
  EyeOff,
  FileText,
  FolderGit2,
  GitMerge,
  Layers,
  LayoutGrid,
  List,
  MessageSquarePlus,
  Pause,
  Play,
  RefreshCw,
  Rocket,
  Settings,
  Shield,
  ShieldOff,
  Square,
  UserPlus,
  Users,
} from 'lucide-react'
import { toast } from 'sonner'
import { StateDot } from '@/components/state-dot'
import {
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from '@/components/ui/command'
import { api } from '@/lib/api'
import { stateLabel } from '@/lib/status'
import { useStore } from '@/store'
import { useAttentionRuns, useCapability } from '@/store/hooks'

/**
 * Everything the palette can do. Jumping is local; the steering verbs go
 * straight to the gateway and the event stream reports the result, so nothing
 * here writes run state into the store. Rendered inside CommandDialog, which
 * supplies the cmdk root.
 */
export function PaletteBody({
  onDone,
  onTemplates,
}: {
  onDone: () => void
  // The template form's open state lives with the dialog host, not the
  // store: store dialogs know only the launch and inject forms.
  onTemplates: () => void
}) {
  const runs = useAttentionRuns()
  const sessions = useStore((s) => s.sessions)
  const members = useStore((s) => s.members)
  const route = useStore((s) => s.route)
  const navigate = useStore((s) => s.navigate)
  const ackAll = useStore((s) => s.ackAll)
  const showIdle = useStore((s) => s.showIdle)
  const toggleIdle = useStore((s) => s.toggleIdle)
  const openDialog = useStore((s) => s.openPaletteDialog)
  const pausedRuns = useStore((s) => s.pausedRuns)
  const cap = useCapability()

  // Steering acts on the run the centre view is showing, whichever of the run
  // detail routes is showing it - the terminal tab is exactly where a human
  // decides to steer. From the board no run is in view: reveal one first.
  const focused = route.params.runId
    ? runs.find((r) => r.run.id === route.params.runId)
    : undefined

  const act = (verb: string, call: Promise<unknown>) => {
    onDone()
    void call.then(
      () => toast.success(verb),
      (err: unknown) => toast.error(`${verb} failed: ${message(err)}`),
    )
  }

  const go = (name: string, params?: Record<string, string>) => {
    onDone()
    navigate(name, params)
  }

  const runID = focused?.run.id ?? ''
  const finished = focused?.state === 'done' || focused?.state === 'failed'
  // Undefined means nobody knows this run's pause state: hydration seeds it
  // from the run list's `paused` wire field, but a legacy gateway sends none,
  // so there a reloaded tab knows no run's state until a pause or resume
  // event arrives. Offer neither verb rather than the one the server would
  // refuse. See the paused-badge gap in docs/dashboard-frontend.md.
  const paused = pausedRuns[runID]

  return (
    <>
      <CommandInput placeholder="Jump to a run, or type a command..." />
      <CommandList>
        <CommandEmpty>Nothing matches.</CommandEmpty>

        {focused && (
          <>
            <CommandGroup heading={focused.run.task}>
              {!finished && (
                <>
                  {paused === true && (
                    <CommandItem onSelect={() => act('Resumed', api.runResume(runID))}>
                      <Play />
                      Resume run
                    </CommandItem>
                  )}
                  {paused === false && (
                    <CommandItem onSelect={() => act('Paused', api.runPause(runID))}>
                      <Pause />
                      Pause run
                    </CommandItem>
                  )}
                  <CommandItem onSelect={() => { onDone(); openDialog('inject', runID) }}>
                    <MessageSquarePlus />
                    Inject a message...
                  </CommandItem>
                  <CommandItem
                    onSelect={() => act('Closed as merged', api.runClose(runID, 'merged'))}
                  >
                    <GitMerge />
                    Close as merged
                  </CommandItem>
                  <CommandItem
                    onSelect={() =>
                      act('Closed as abandoned', api.runClose(runID, 'abandoned'))
                    }
                  >
                    <Archive />
                    Close as abandoned
                  </CommandItem>
                  <CommandItem onSelect={() => act('Killed', api.runKill(runID))}>
                    <Square />
                    Kill run
                  </CommandItem>
                </>
              )}
              {cap.hasMethod('run.protect') && (
                <CommandItem
                  onSelect={() =>
                    act(
                      focused.run.protected ? 'Unprotected' : 'Protected',
                      api.runProtect(runID, !focused.run.protected),
                    )
                  }
                >
                  {focused.run.protected ? <ShieldOff /> : <Shield />}
                  {focused.run.protected ? 'Unprotect run' : 'Protect run'}
                </CommandItem>
              )}
              {finished && cap.hasMethod('run.relaunch') && (
                <CommandItem onSelect={() => act('Relaunched', api.runRelaunch(runID))}>
                  <RefreshCw />
                  Relaunch run
                </CommandItem>
              )}
              {cap.hasLocal('pull') && (
                <CommandItem onSelect={() => act('Pulled branch', api.localPull(runID))}>
                  <Download />
                  Pull branch
                </CommandItem>
              )}
              {/* Viewers cannot own a run, so the server refuses a handoff
                  to one; do not offer what will be refused. */}
              {Object.values(members)
                .filter(
                  (m) =>
                    m.id !== focused.run.member_id &&
                    !m.pending &&
                    m.role !== 'viewer',
                )
                .map((m) => (
                  <CommandItem
                    key={m.id}
                    value={`hand off ${m.display_name} ${m.id}`}
                    onSelect={() =>
                      act(`Handed off to ${m.display_name}`, api.runHandoff(runID, m.id))
                    }
                  >
                    <UserPlus />
                    Hand off to {m.display_name}
                  </CommandItem>
                ))}
            </CommandGroup>
            <CommandSeparator />
          </>
        )}

        <CommandGroup heading="Board">
          <CommandItem onSelect={() => go('board')}>
            <LayoutGrid />
            Open the run board
          </CommandItem>
          <CommandItem onSelect={() => go('overview')}>
            <List />
            Open every run as a list
          </CommandItem>
          <CommandItem onSelect={() => { onDone(); openDialog('launch') }}>
            <Rocket />
            Launch a run...
          </CommandItem>
          <CommandItem onSelect={() => { onDone(); onTemplates() }}>
            <FileText />
            Launch from a template...
          </CommandItem>
          <CommandItem onSelect={() => { onDone(); ackAll() }}>
            <CheckCheck />
            Mark all runs seen
          </CommandItem>
          <CommandItem onSelect={() => { onDone(); toggleIdle() }}>
            {showIdle ? <EyeOff /> : <Eye />}
            {showIdle ? 'Hide' : 'Show'} idle sessions
          </CommandItem>
        </CommandGroup>

        {(cap.hasMethod('member.list') ||
          cap.hasMethod('workspace.add') ||
          cap.hasMethod('template.save') ||
          cap.hasMethod('agent.list') ||
          cap.hasLocal('daemon.status') ||
          cap.hasLocal('link.status')) && (
          <CommandGroup heading="Go to">
            {cap.hasMethod('member.list') && (
              <CommandItem onSelect={() => go('members')}>
                <Users />
                Members
              </CommandItem>
            )}
            {cap.hasMethod('workspace.add') && (
              <CommandItem onSelect={() => go('workspaces')}>
                <FolderGit2 />
                Workspaces
              </CommandItem>
            )}
            {cap.hasMethod('template.save') && (
              <CommandItem onSelect={() => go('templates')}>
                <FileText />
                Templates
              </CommandItem>
            )}
            {cap.hasMethod('agent.list') && (
              <CommandItem onSelect={() => go('agents')}>
                <Bot />
                Agents
              </CommandItem>
            )}
            {cap.hasLocal('daemon.status') && (
              <CommandItem onSelect={() => go('settings')}>
                <Settings />
                Settings
              </CommandItem>
            )}
            {cap.hasLocal('link.status') && (
              <CommandItem onSelect={() => go('onboarding')}>
                <Compass />
                Onboarding
              </CommandItem>
            )}
          </CommandGroup>
        )}

        <CommandGroup heading="Runs">
          {runs.map(({ run, state }) => (
            <CommandItem
              key={run.id}
              value={`${run.task} ${run.branch} ${run.harness} ${sessions[run.session_id]?.name ?? ''} ${run.id}`}
              onSelect={() => go('run', { runId: run.id })}
            >
              <StateDot state={state} />
              <span className="truncate">{run.task}</span>
              <span className="ml-auto shrink-0 text-xs text-muted-foreground">
                {stateLabel[state]}
              </span>
            </CommandItem>
          ))}
        </CommandGroup>

        <CommandGroup heading="Sessions">
          {Object.values(sessions).map((s) => (
            <CommandItem
              key={s.id}
              value={`${s.name} ${s.base_branch} ${s.id}`}
              onSelect={() => go('session', { sessionId: s.id })}
            >
              <Layers />
              <span className="truncate">{s.name}</span>
            </CommandItem>
          ))}
        </CommandGroup>
      </CommandList>
    </>
  )
}

export function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}
