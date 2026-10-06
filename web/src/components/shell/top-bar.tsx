import { PanelLeft, Plus, Search } from '@/components/icons'
import { ConnectionLine } from '@/components/shell/connection'
import { Button } from '@/components/ui/button'
import { canLaunch } from '@/lib/commands'
import { runLabel } from '@/lib/status'
import { surfaces } from '@/lib/surfaces'
import { useStore } from '@/store'
import { useCapability, useNeedsYouCount, useSelfRole } from '@/store/hooks'

const titles: Record<string, string> = {
  overview: 'All workspaces',
  missions: 'Swarms',
}

export function TopBar() {
  const cap = useCapability()
  const role = useSelfRole()
  const route = useStore((s) => s.route)
  const run = useStore((s) => (s.route.params.runId ? s.runs[s.route.params.runId] : undefined))
  const workspace = useStore((s) => (s.route.name === 'workspace' ? s.workspaces[s.route.params.workspaceId ?? ''] : undefined))
  const drawerOpen = useStore((s) => s.sidebarDrawerOpen)
  const setDrawerOpen = useStore((s) => s.setSidebarDrawerOpen)
  const togglePalette = useStore((s) => s.togglePalette)
  const openDialog = useStore((s) => s.openPaletteDialog)
  const needsYou = useNeedsYouCount()
  const iconLaunch = useStore((s) => s.headerPrimaries > 0 || s.route.name === 'run')
  const title = route.name === 'run'
    ? 'Run'
    : run
    ? runLabel(run)
    : workspace?.name ?? titles[route.name] ?? surfaces(cap, true).find((surface) => surface.name === route.name)?.label ?? 'Aether'

  return (
    <header className="shrink-0 border-b border-seam bg-chrome pt-[var(--safe-top)] pr-[env(safe-area-inset-right)] pl-[env(safe-area-inset-left)]">
      <div className="flex h-[calc(var(--top-bar-height)-1px)] items-center gap-1 px-1">
        <span className="relative shrink-0">
          <Button
            id="sidebar-drawer-trigger"
            variant="ghost"
            size="icon"
            label={needsYou > 0 ? `Open sidebar, ${needsYou} ${needsYou === 1 ? 'run needs' : 'runs need'} you` : 'Open sidebar'}
            aria-expanded={drawerOpen}
            aria-controls="sidebar-drawer"
            onClick={() => setDrawerOpen(true)}
          >
            <PanelLeft />
          </Button>
          {needsYou > 0 && (
            <span aria-hidden className="pointer-events-none absolute top-2 right-2 size-2 rounded-full bg-state-needs-you" />
          )}
        </span>
        <p className="min-w-0 flex-1 truncate text-ui font-medium text-text">{title}</p>
        <Button variant="ghost" size="icon" label="Search" onClick={() => togglePalette(true)}>
          <Search />
        </Button>
        {canLaunch({ cap, role }) && (iconLaunch ? (
          <Button variant="ghost" size="icon" label="New run" onClick={() => openDialog('launch')}>
            <Plus />
          </Button>
        ) : (
          <Button size="sm" onClick={() => openDialog('launch')}>
            <Plus />
            New run
          </Button>
        ))}
      </div>
      <ConnectionLine className="px-3 pb-1.5" />
    </header>
  )
}
