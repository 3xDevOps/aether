import { useRef, useState } from 'react'
import { FolderGit2 } from '@/components/icons'
import { StatusDot } from '@/components/ui/status-dot'
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
import { useCapability, useListedRuns, useSelf } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

// Browsing shows the most urgent runs; a search reaches every run.
const browseRunLimit = 50

const destinationCommandIDs: Record<string, true> = {
  board: true,
  overview: true,
  launch: true,
  swarm: true,
  template: true,
  inject: true,
  forward: true,
  close: true,
}

/** Rendered inside CommandDialog, which supplies the cmdk root. */
export function PaletteBody({
  onDone,
  onTemplates,
  onConfirm,
}: {
  onDone: (restoreFocus?: boolean) => void
  // The template form's open state lives with the dialog host, not the
  // store: store dialogs know the launch, inject and forward forms.
  onTemplates: () => void
  onConfirm: (run: RunRecord, command: Command & { confirm: NonNullable<Command['confirm']> }) => void
}) {
  const runs = useListedRuns(useStore((s) => s.activeWorkspace))
  const runMap = useStore((s) => s.runs)
  const workspaces = useStore((s) => s.workspaces)
  const members = useStore((s) => s.members)
  const route = useStore((s) => s.route)
  const navigate = useStore((s) => s.navigate)
  const pausedRuns = useStore((s) => s.pausedRuns)
  const cap = useCapability()
  const self = useSelf()
  const [search, setSearch] = useState('')
  const selected = useRef<Command | null>(null)
  const complete = () => {
    const command = selected.current
    selected.current = null
    onDone(!command || !destinationCommandIDs[command.id])
  }
  const perform = useCommandRunner({ onDone: complete, onTemplates })

  // The run map, not the run list: the list drops archived final runs, whose
  // own page still needs its commands (Restore among them).
  const focused = route.params.runId ? runMap[route.params.runId] : undefined

  const goTo = surfaces(cap).filter((surface) => surface.name !== 'board' && surface.place !== 'link').map((surface) => ({
    ...surface,
    value: `${surface.label} ${surface.name} ${surface.keywords ?? ''}`,
  }))
  const board = boardCommands({ cap, self })
  const navigationCommands = board.filter((command) => command.id === 'board' || command.id === 'overview')
  const boardActions = board.filter((command) => command.id !== 'board' && command.id !== 'overview')
  // The value is what cmdk scores: the label the row shows, never the full
  // task text. The id keeps two otherwise identical rows distinct.
  const runItems = (search ? runs : runs.slice(0, browseRunLimit)).map(({ run, state }) => ({
    run,
    state,
    value: `${runLabel(run)} ${run.branch} ${run.harness} ${workspaces[run.workspace_id]?.name ?? ''} ${run.id}`,
  }))
  const workspaceItems = Object.values(workspaces).map((workspace) => ({
    workspace,
    value: `${workspace.name} ${workspace.base_branch} ${workspace.id}`,
  }))

  const go = (name: string, params?: Record<string, string>) => {
    onDone(false)
    navigate(name, params)
  }

  const item = (command: Command) => (
    <CommandItem
      key={command.id}
      value={command.value ?? command.label}
      disabled={command.disabled}
      onSelect={() => {
        if (command.confirm && focused) {
          onConfirm(focused, { ...command, confirm: command.confirm })
          return
        }
        selected.current = command
        void perform(command)
      }}
    >
      <command.Icon />
      <span className="min-w-0 flex-1 truncate">{command.label}</span>
      {command.disabled && (
        <span className="shrink-0 text-ui-sm text-muted">Unavailable</span>
      )}
    </CommandItem>
  )

  const focusedContext = focused && {
    run: focused,
    paused: pausedRuns[focused.id],
    cap,
    members,
    self,
    steerOthers: workspaces[focused.workspace_id]?.steer_others,
  }
  const focusedCommands = focusedContext
    ? [...runCommands(focusedContext), ...handoffCommands(focusedContext)]
    : []
  const browseOrder = [
    ...navigationCommands.map((command) => command.value ?? command.label),
    ...goTo.map(({ value }) => value),
    ...runItems.map(({ value }) => value),
    ...workspaceItems.map(({ value }) => value),
    ...focusedCommands.map((command) => command.value ?? command.label),
    ...boardActions.map((command) => command.value ?? command.label),
  ]

  return (
    <>
      <CommandInput
        value={search}
        onValueChange={setSearch}
        placeholder="Search commands, runs, workspaces…"
      />
      <CommandList browseOrder={browseOrder} className="min-h-0 px-1 pb-1">
        <CommandEmpty className="py-4">No commands, runs, or workspaces match.</CommandEmpty>

        <CommandGroup heading="Navigate">
          {navigationCommands.map(item)}
          {goTo.map(({ name, label, Icon, value }) => (
            <CommandItem
              key={name}
              value={value}
              onSelect={() => go(name)}
            >
              <Icon />
              <span className="min-w-0 truncate">{label}</span>
            </CommandItem>
          ))}
        </CommandGroup>

        <CommandGroup heading="Runs">
          {runItems.map(({ run, state, value }) => (
            <CommandItem
              key={run.id}
              value={value}
              onSelect={() => go('run', { runId: run.id })}
              className="items-start py-1"
            >
              <StatusDot tone={state} className="mx-1 mt-1 shrink-0" />
              <span className="min-w-0 flex-1">
                <span className="block truncate">{runLabel(run)}</span>
                <span className="block truncate text-ui-sm text-muted">
                  {workspaces[run.workspace_id]?.name ?? 'Workspace'} · {run.branch || 'No branch'}
                </span>
              </span>
              <span className="shrink-0 text-ui-sm font-medium text-muted">
                {stateLabel[state]}
              </span>
            </CommandItem>
          ))}
        </CommandGroup>

        <CommandGroup heading="Workspaces">
          {workspaceItems.map(({ workspace: w, value }) => (
            <CommandItem
              key={w.id}
              value={value}
              onSelect={() => go('workspace', { workspaceId: w.id })}
            >
              <FolderGit2 />
              <span className="min-w-0 flex-1 truncate">{w.name}</span>
              <span className="max-w-32 truncate text-ui-sm text-muted">{w.base_branch}</span>
            </CommandItem>
          ))}
        </CommandGroup>

        {focusedContext && (
          <>
            <CommandSeparator />
            <CommandGroup heading={`Focused run · ${runLabel(focusedContext.run)}`}>
              {focusedCommands.map(item)}
            </CommandGroup>
          </>
        )}
        <CommandGroup heading="Board actions">
          {boardActions.map(item)}
        </CommandGroup>
      </CommandList>
    </>
  )
}
