import {
  Bot,
  Compass,
  FileText,
  FolderGit2,
  Settings,
  Users,
} from 'lucide-react'
import { StateDot } from '@/components/state-dot'
import {
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from '@/components/ui/command'
import {
  boardCommands,
  handoffCommands,
  runCommands,
  useCommandRunner,
  type Command,
} from '@/lib/commands'
import { runLabel, stateLabel } from '@/lib/status'
import { useStore } from '@/store'
import { useAttentionRuns, useCapability, useSelf } from '@/store/hooks'

/**
 * Everything the palette can do. The verbs themselves live in
 * `src/lib/commands.ts` so the visible buttons offer exactly the same list;
 * jumping is local to the palette. Rendered inside CommandDialog, which
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
  const workspaces = useStore((s) => s.workspaces)
  const members = useStore((s) => s.members)
  const route = useStore((s) => s.route)
  const navigate = useStore((s) => s.navigate)
  const pausedRuns = useStore((s) => s.pausedRuns)
  const cap = useCapability()
  const self = useSelf()
  const perform = useCommandRunner({ onDone, onTemplates })

  // Steering acts on the run the centre view is showing, whichever of the run
  // detail routes is showing it - the terminal tab is exactly where a human
  // decides to steer. From the board no run is in view: reveal one first.
  const focused = route.params.runId
    ? runs.find((r) => r.run.id === route.params.runId)
    : undefined

  const go = (name: string, params?: Record<string, string>) => {
    onDone()
    navigate(name, params)
  }

  const item = (command: Command) => (
    <CommandItem
      key={command.id}
      value={command.value}
      onSelect={() => void perform(command)}
    >
      <command.Icon />
      {command.label}
    </CommandItem>
  )

  const focusedContext = focused && {
    run: focused.run,
    paused: pausedRuns[focused.run.id],
    cap,
    members,
    self,
    steerOthers: workspaces[focused.run.workspace_id]?.steer_others,
  }

  return (
    <>
      <CommandInput placeholder="Jump to a run, or type a command..." />
      <CommandList>
        <CommandEmpty>Nothing matches.</CommandEmpty>

        {focusedContext && (
          <>
            <CommandGroup heading={runLabel(focusedContext.run)}>
              {runCommands(focusedContext).map(item)}
              {handoffCommands(focusedContext).map(item)}
            </CommandGroup>
            <CommandSeparator />
          </>
        )}

        <CommandGroup heading="Board">
          {boardCommands({ cap, role: self.role }).map(item)}
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
                Manage workspaces
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
              value={`${run.task} ${run.branch} ${run.harness} ${workspaces[run.workspace_id]?.name ?? ''} ${run.id}`}
              onSelect={() => go('run', { runId: run.id })}
            >
              <StateDot state={state} />
              <span className="truncate">{runLabel(run)}</span>
              <span className="ml-auto shrink-0 text-xs text-muted-foreground">
                {stateLabel[state]}
              </span>
            </CommandItem>
          ))}
        </CommandGroup>

        <CommandGroup heading="Workspaces">
          {Object.values(workspaces).map((w) => (
            <CommandItem
              key={w.id}
              value={`${w.name} ${w.base_branch} ${w.id}`}
              // Opening a workspace also makes it the active scope, so the
              // sidebar, the board and every launch follow.
              onSelect={() => go('workspace', { workspaceId: w.id })}
            >
              <FolderGit2 />
              <span className="truncate">{w.name}</span>
            </CommandItem>
          ))}
        </CommandGroup>
      </CommandList>
    </>
  )
}
