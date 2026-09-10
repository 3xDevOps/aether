import { FolderGit2 } from 'lucide-react'
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
  const perform = useCommandRunner({ onDone, onTemplates })

  // Steering acts on the run the centre view is showing, whichever of the run
  // detail routes is showing it - the terminal tab is exactly where a human
  // decides to steer. From the board no run is in view: reveal one first.
  const focused = route.params.runId
    ? runs.find((r) => r.run.id === route.params.runId)
    : undefined

  const goTo = surfaces(cap)

  const go = (name: string, params?: Record<string, string>) => {
    onDone()
    navigate(name, params)
  }

  const item = (command: Command) => (
    <CommandItem
      key={command.id}
      value={command.value}
      disabled={command.disabled}
      onSelect={() => void perform(command)}
      className="min-h-11 gap-3 px-3 py-2"
    >
      <span className="grid size-8 shrink-0 place-items-center rounded-md bg-muted text-muted-foreground transition-colors group-data-[selected=true]:bg-background group-data-[selected=true]:text-foreground">
        <command.Icon />
      </span>
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
      <CommandInput
        placeholder="Search commands, runs, workspaces..."
        className="h-11 px-2 text-[15px]"
      />
      <CommandList className="min-h-0 max-h-[min(520px,calc(100dvh-9rem))] px-1 pb-2">
        <CommandEmpty className="py-10">No commands, runs, or workspaces match.</CommandEmpty>

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
                className="min-h-11 gap-3 px-3 py-2"
              >
                <span className="grid size-8 shrink-0 place-items-center rounded-md bg-muted text-muted-foreground">
                  <Icon />
                </span>
                <span className="truncate">{label}</span>
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
              className="min-h-11 gap-3 px-3 py-2"
            >
              <StateDot state={state} decorative className="mx-1 shrink-0" />
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
              className="min-h-11 gap-3 px-3 py-2"
            >
              <span className="grid size-8 shrink-0 place-items-center rounded-md bg-muted text-muted-foreground">
                <FolderGit2 />
              </span>
              <span className="min-w-0 flex-1 truncate">{w.name}</span>
              <span className="max-w-32 truncate text-xs text-muted-foreground">{w.base_branch}</span>
            </CommandItem>
          ))}
        </CommandGroup>
      </CommandList>
    </>
  )
}
