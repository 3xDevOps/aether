import {
  Bot,
  Compass,
  FileText,
  FolderGit2,
  LayoutGrid,
  PanelLeftClose,
  PanelLeftOpen,
  Settings,
  Users,
} from 'lucide-react'
import { useCallback } from 'react'
import { StateDot } from '@/components/state-dot'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useDelayed } from '@/lib/hooks'
import { runLabel } from '@/lib/status'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import { isUnseen } from '@/store/board'
import { useAttentionCount, useCapability, useSidebarGroups } from '@/store/hooks'
import type { SidebarGroup, SidebarRun } from '@/store/selectors'

export function Sidebar() {
  const collapsed = useStore((s) => s.sidebarCollapsed)
  const width = useStore((s) => s.sidebarWidth)
  const toggleSidebar = useStore((s) => s.toggleSidebar)
  const setSidebarWidth = useStore((s) => s.setSidebarWidth)

  const startResize = useCallback(
    (e: React.PointerEvent) => {
      e.preventDefault()
      const move = (ev: PointerEvent) => setSidebarWidth(ev.clientX)
      const stop = () => {
        window.removeEventListener('pointermove', move)
        window.removeEventListener('pointerup', stop)
      }
      window.addEventListener('pointermove', move)
      window.addEventListener('pointerup', stop)
    },
    [setSidebarWidth],
  )

  if (collapsed) {
    return (
      <aside className="flex w-10 shrink-0 flex-col items-center border-r py-2">
        <Button
          variant="ghost"
          size="icon"
          aria-label="Expand sidebar"
          onClick={toggleSidebar}
        >
          <PanelLeftOpen />
        </Button>
      </aside>
    )
  }

  return (
    <aside
      className="relative flex shrink-0 flex-col border-r"
      style={{ width }}
      aria-label="Runs"
    >
      <WorkspaceSwitcher onCollapse={toggleSidebar} />
      <SidebarHeader />
      <RunTree />
      <NavSection />
      <div
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize sidebar"
        onPointerDown={startResize}
        className="absolute inset-y-0 -right-1 w-2 cursor-col-resize hover:bg-accent"
      />
    </aside>
  )
}

/**
 * The scoping control, above everything it scopes. A single workspace needs
 * no picker, so it renders as a plain label: the affordance appears only
 * when there is a choice to make.
 */
function WorkspaceSwitcher({ onCollapse }: { onCollapse: () => void }) {
  const workspaces = useStore((s) => s.workspaces)
  const active = useStore((s) => s.activeWorkspace)
  const setActiveWorkspace = useStore((s) => s.setActiveWorkspace)
  const list = Object.values(workspaces)
  const current = workspaces[active]

  return (
    <div className="flex items-center gap-2 border-b px-2 py-1.5">
      <FolderGit2 className="size-4 shrink-0 text-muted-foreground" />
      {list.length > 1 ? (
        <select
          aria-label="Workspace"
          className="min-w-0 flex-1 rounded-md border bg-background px-2 py-1 text-sm"
          value={active}
          onChange={(e) => setActiveWorkspace(e.target.value)}
        >
          {list.map((workspace) => (
            <option key={workspace.id} value={workspace.id}>
              {workspace.name}
            </option>
          ))}
        </select>
      ) : (
        <span className="flex min-w-0 flex-1 flex-col items-start leading-tight">
          <span className="truncate text-sm font-medium">
            {current?.name ?? 'No workspace'}
          </span>
          {current && (
            <span className="truncate text-[11px] text-muted-foreground">
              {current.base_branch}
            </span>
          )}
        </span>
      )}
      <Button
        variant="ghost"
        size="icon"
        aria-label="Collapse sidebar"
        onClick={onCollapse}
      >
        <PanelLeftClose />
      </Button>
    </div>
  )
}

function SidebarHeader() {
  const groupBy = useStore((s) => s.groupBy)
  const setGroupBy = useStore((s) => s.setGroupBy)
  const navigate = useStore((s) => s.navigate)
  return (
    <div className="flex items-center gap-1 border-b px-2 py-1.5">
      <span className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
        Runs
      </span>
      <AttentionBadge />
      <div className="ml-auto flex items-center gap-1">
        <Button
          variant="ghost"
          size="icon"
          title="Run board"
          aria-label="Run board"
          onClick={() => navigate('board')}
        >
          <LayoutGrid />
        </Button>
        <Button
          variant="ghost"
          size="sm"
          title="Group runs"
          onClick={() => setGroupBy(groupBy === 'status' ? 'member' : 'status')}
        >
          {groupBy === 'status' ? 'Status' : 'Member'}
        </Button>
      </div>
    </div>
  )
}

/**
 * How many runs are waiting on a human. The runs below are already sorted
 * worst-first, so this is not navigation - it is the count a member needs
 * when the sidebar is scrolled, or when a stall lands while they are
 * elsewhere in the app.
 */
function AttentionBadge() {
  const count = useAttentionCount()
  if (count === 0) return null
  return (
    <span
      aria-label={`${count} ${count === 1 ? 'run needs' : 'runs need'} you`}
      title="Runs waiting on a human"
      className="rounded-full bg-state-needs-attention/15 px-1.5 text-[11px] font-medium text-state-needs-attention"
    >
      {count}
    </span>
  )
}

function RunTree() {
  const groups = useSidebarGroups()
  const hydrated = useStore((s) => s.hydrated)
  const error = useStore((s) => s.hydrationError)
  const dead = useStore((s) => s.streamDead)
  const unreachable = error !== null
  const loading = useDelayed(!hydrated && !unreachable && groups.length === 0)

  if (groups.length === 0) {
    return (
      <div className="flex-1 space-y-2 overflow-y-auto p-2">
        {unreachable ? (
          <p className="px-1 py-2 text-xs text-muted-foreground">
            {dead ? error : 'Cannot reach the server. Retrying.'}
          </p>
        ) : loading ? (
          <>
            <Skeleton className="h-6 w-full" />
            <Skeleton className="h-6 w-4/5" />
            <Skeleton className="h-6 w-3/5" />
          </>
        ) : (
          hydrated && (
            <p className="px-1 py-2 text-xs text-muted-foreground">
              No runs yet.
            </p>
          )
        )}
      </div>
    )
  }

  return (
    <div className="flex-1 overflow-y-auto py-1">
      {groups.map((group) => (
        <Group key={group.key} group={group} />
      ))}
    </div>
  )
}

/**
 * Entry points for the admin and desktop surfaces. Every link is gated on
 * the capability that powers its view, so a gateway that cannot serve a
 * surface never shows the way in. Members is the exception that reads:
 * member.list is on the remote allowlist, the roster is worth seeing, and
 * the admin verbs inside it are gated one by one on role as well.
 */
function NavSection() {
  const cap = useCapability()
  const navigate = useStore((s) => s.navigate)
  const route = useStore((s) => s.route)
  const links: { name: string; label: string; Icon: typeof Users }[] = []
  if (cap.hasMethod('member.list'))
    links.push({ name: 'members', label: 'Members', Icon: Users })
  if (cap.hasMethod('workspace.add'))
    links.push({ name: 'workspaces', label: 'Manage workspaces', Icon: FolderGit2 })
  if (cap.hasMethod('template.save'))
    links.push({ name: 'templates', label: 'Templates', Icon: FileText })
  if (cap.hasMethod('agent.list'))
    links.push({ name: 'agents', label: 'Agents', Icon: Bot })
  if (cap.hasLocal('link.status'))
    links.push({ name: 'onboarding', label: 'Onboarding', Icon: Compass })
  if (cap.hasLocal('daemon.status'))
    links.push({ name: 'settings', label: 'Settings', Icon: Settings })
  if (links.length === 0) return null
  return (
    <nav aria-label="Surfaces" className="shrink-0 border-t py-1">
      {links.map(({ name, label, Icon }) => (
        <button
          key={name}
          type="button"
          onClick={() => navigate(name)}
          className={cn(
            'flex w-full items-center gap-2 px-2 py-1 text-left text-sm hover:bg-accent/60',
            route.name === name && 'bg-accent',
          )}
        >
          <Icon className="size-3.5 text-muted-foreground" />
          {label}
        </button>
      ))}
    </nav>
  )
}

function Group({ group }: { group: SidebarGroup }) {
  return (
    <section className="mb-1">
      <h2 className="px-2 py-1 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
        {group.label}
      </h2>
      {group.runs.map((run) => (
        <RunRow key={run.run.id} entry={run} />
      ))}
    </section>
  )
}

function RunRow({ entry }: { entry: SidebarRun }) {
  const navigate = useStore((s) => s.navigate)
  const route = useStore((s) => s.route)
  // Acks are app-wide, so a row mutes at the same moment its board card does.
  const unseen = useStore((s) => isUnseen(s.acked, entry.run))
  const selected = route.name === 'run' && route.params.runId === entry.run.id
  return (
    <button
      type="button"
      onClick={() => navigate('run', { runId: entry.run.id })}
      style={{ borderLeftColor: entry.owner?.color }}
      className={cn(
        'flex w-full items-center gap-2 border-l-2 py-1 pr-2 pl-4 text-left text-xs hover:bg-accent/60',
        selected ? 'bg-accent' : 'border-l-transparent',
        unseen ? 'font-medium' : 'text-muted-foreground',
      )}
    >
      <StateDot state={entry.state} />
      <span className="truncate">{runLabel(entry.run)}</span>
      <span className="ml-auto shrink-0 text-muted-foreground">
        {entry.run.harness}
      </span>
    </button>
  )
}
