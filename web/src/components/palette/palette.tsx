import { FolderGit2 } from 'lucide-react'
import { useRef } from 'react'
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
import { surfaces } from '@/lib/surfaces'
import { useStore } from '@/store'
import { useAttentionRuns, useCapability, useSelf } from '@/store/hooks'

const destinationCommandIDs: Record<string, true> = {
  board: true,
  overview: true,
  launch: true,
  template: true,
  inject: true,
  forward: true,
  close: true,
}

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
  onDone: (restoreFocus?: boolean) => void
  // The template form's open state lives with the dialog host, not the
  // store: store dialogs know the launch, inject and forward forms.
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
  const selected = useRef<Command | null>(null)
  const complete = () => {
    const command = selected.current
    selected.current = null
    onDone(!command || !destinationCommandIDs[command.id])
  }
  const perform = useCommandRunner({ onDone: complete, onTemplates })

  // Steering acts on the run the centre view is showing, whichever of the run
  // detail routes is showing it - the terminal tab is exactly where a human
  // decides to steer. From the board no run is in view: reveal one first.
  const focused = route.params.runId
    ? runs.find((r) => r.run.id === route.params.runId)
    : undefined

  const goTo = surfaces(cap)

  const go = (name: string, params?: Record<string, string>) => {
    onDone(false)
    navigate(name, params)
  }

  const item = (command: Command) => (
    <CommandItem
      key={command.id}
      value={command.value}
      disabled={command.disabled}
      onSelect={() => {
        selected.current = command
        void perform(command)
      }}
    >
      <command.Icon />
      <span className="min-w-0 flex-1 truncate">{command.label}</span>
      {command.disabled && (
        <span className="shrink-0 text-xs text-muted-foreground">Unavailable</span>
      )}
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
      <CommandInput placeholder="Search commands, runs, workspaces..." />
      <CommandList className="min-h-0 px-1 pb-1">
        <CommandEmpty className="py-4">No commands, runs, or workspaces match.</CommandEmpty>

        {focusedContext && (
          <>
            <CommandGroup heading={`Focused run · ${runLabel(focusedContext.run)}`}>
              {runCommands(focusedContext).map(item)}
              {handoffCommands(focusedContext).map(item)}
            </CommandGroup>
            <CommandSeparator />
          </>
        )}

        <CommandGroup heading="Board actions">
          {boardCommands({ cap, role: self.role }).map(item)}
        </CommandGroup>

        {goTo.length > 0 && (
          <CommandGroup heading="Navigate">
            {goTo.map(({ name, label, Icon }) => (
              <CommandItem
                key={name}
                value={`${label} ${name}`}
                onSelect={() => go(name)}
              >
                <Icon />
                <span className="min-w-0 truncate">{label}</span>
              </CommandItem>
            ))}
          </CommandGroup>
        )}

        <CommandGroup heading="Attention runs">
          {runs.map(({ run, state }) => (
            <CommandItem
              key={run.id}
              value={`${run.task} ${run.branch} ${run.harness} ${workspaces[run.workspace_id]?.name ?? ''} ${run.id}`}
              onSelect={() => go('terminal', { runId: run.id })}
              className="items-start py-1"
            >
              <StateDot state={state} decorative className="mx-1 mt-1 shrink-0" />
              <span className="min-w-0 flex-1">
                <span className="block truncate">{runLabel(run)}</span>
                <span className="block truncate text-xs text-muted-foreground">
                  {workspaces[run.workspace_id]?.name ?? 'Workspace'} · {run.branch || 'No branch'}
                </span>
              </span>
              <span className="shrink-0 text-xs font-medium text-muted-foreground">
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
              onSelect={() => go('workspace', { workspaceId: w.id })}
            >
              <FolderGit2 />
              <span className="min-w-0 flex-1 truncate">{w.name}</span>
              <span className="max-w-32 truncate text-xs text-muted-foreground">{w.base_branch}</span>
            </CommandItem>
          ))}
        </CommandGroup>
      </CommandList>
    </>
  )
}
