import { ChevronsUpDown } from '@/components/icons'
import { Button } from '@/components/ui/button'
import {
  Menu,
  MenuContent,
  MenuItem,
  MenuLabel,
  MenuRadioGroup,
  MenuRadioItem,
  MenuSeparator,
  MenuTrigger,
} from '@/components/ui/menu'
import { useStore } from '@/store'
import { useCapability, useNeedsYouByWorkspace } from '@/store/hooks'

function needsYouLabel(count: number): string {
  return `${count} ${count === 1 ? 'run needs' : 'runs need'} you`
}

export function WorkspaceSwitcher() {
  const workspaces = useStore((s) => s.workspaces)
  const active = useStore((s) => s.activeWorkspace)
  const setActiveWorkspace = useStore((s) => s.setActiveWorkspace)
  const navigate = useStore((s) => s.navigate)
  const caps = useCapability()
  const counts = useNeedsYouByWorkspace()
  const list = Object.values(workspaces).sort((a, b) => a.name.localeCompare(b.name))
  const current = workspaces[active]
  const total = Object.values(counts).reduce((sum, n) => sum + n, 0)

  const name = (
    <span className="flex min-w-0 flex-1 items-baseline gap-1.5 text-left">
      <span className="truncate font-medium text-text">{current?.name ?? 'No workspace'}</span>
      {current && <span className="truncate text-ui-sm text-muted">{current.base_branch}</span>}
    </span>
  )
  const badge = total > 0 && (
    <span role="img" aria-label={needsYouLabel(total)} className="shrink-0 text-ui-sm font-medium tabular-nums text-state-needs-you">
      {total}
    </span>
  )

  if (list.length <= 1 && !caps.hasMethod('workspace.list')) {
    return (
      <div className="flex h-7 min-w-0 flex-1 items-center gap-2 px-2 text-ui">
        {name}
        {badge}
      </div>
    )
  }

  return (
    <Menu>
      <MenuTrigger asChild>
        <Button variant="ghost" aria-label={`Workspace: ${current?.name ?? 'none'}`} className="min-w-0 flex-1 justify-start">
          {name}
          {badge}
          <ChevronsUpDown />
        </Button>
      </MenuTrigger>
      <MenuContent align="start" className="w-64">
        {list.length > 1 && (
          <>
            <MenuLabel>Workspaces</MenuLabel>
            <MenuRadioGroup value={active} onValueChange={setActiveWorkspace}>
              {list.map((workspace) => {
                const count = counts[workspace.id] ?? 0
                return (
                  <MenuRadioItem
                    key={workspace.id}
                    value={workspace.id}
                    aria-label={count > 0 ? `${workspace.name}, ${needsYouLabel(count)}` : workspace.name}
                  >
                    <span className="min-w-0 flex-1 truncate">{workspace.name}</span>
                    {count > 0 && <span className="tabular-nums text-state-needs-you">{count}</span>}
                  </MenuRadioItem>
                )
              })}
            </MenuRadioGroup>
            <MenuItem onSelect={() => navigate('overview')}>All workspaces</MenuItem>
            <MenuSeparator />
          </>
        )}
        {caps.hasMethod('workspace.list') && (
          <MenuItem onSelect={() => navigate('workspaces')}>Manage workspaces</MenuItem>
        )}
        {current && (
          <MenuItem onSelect={() => navigate('workspace', { workspaceId: current.id })}>Repository</MenuItem>
        )}
      </MenuContent>
    </Menu>
  )
}
