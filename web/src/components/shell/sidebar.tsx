import {
  FolderGit2,
  LayoutGrid,
  List,
  PanelLeftClose,
  PanelLeftOpen,
  Rocket,
} from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { StateDot } from '@/components/state-dot'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { canLaunch } from '@/lib/commands'
import { useDelayed, useDrag } from '@/lib/hooks'
import { splitterTarget } from '@/lib/keys'
import { runLabel } from '@/lib/status'
import { surfaces, type Surface } from '@/lib/surfaces'
import { cn, focusRing } from '@/lib/utils'
import { isRunRoute } from '@/routes/terminal/tabs'
import { useStore } from '@/store'
import { pendingApprovals } from '@/store/approvals'
import { isUnseen } from '@/store/board'
import {
  useAttentionCount,
  useCapability,
  useSelfRole,
  useSidebarGroups,
} from '@/store/hooks'
import type { SidebarGroup, SidebarRun } from '@/store/selectors'
import { maxSidebarWidth, minSidebarWidth } from '@/store/ui'

/**
 * The desktop shell cannot open a window narrower than 960px, so this matches
 * at the smallest window there is: a sidebar at its default width plus three
 * board columns does not fit in it, and the rail is what keeps the board
 * readable.
 */
const narrowQuery = '(max-width: 1000px)'

export function Sidebar() {
  const collapsed = useStore((s) => s.sidebarCollapsed)
  const width = useStore((s) => s.sidebarWidth)
  const toggleSidebar = useStore((s) => s.toggleSidebar)
  const setSidebarWidth = useStore((s) => s.setSidebarWidth)
  const [autoCollapsed, setAutoCollapsed] = useState(
    () => window.matchMedia?.(narrowQuery).matches ?? false,
  )
  // Shadows sidebarCollapsed while the window is narrow, so the toggle answers
  // the viewport without writing the preference the member stored.
  const [expandedNarrow, setExpandedNarrow] = useState(false)

  useEffect(() => {
    const media = window.matchMedia?.(narrowQuery)
    if (!media) return
    const apply = (e: MediaQueryListEvent) => {
      setAutoCollapsed(e.matches)
      if (!e.matches) setExpandedNarrow(false)
    }
    media.addEventListener('change', apply)
    return () => media.removeEventListener('change', apply)
  }, [])

  const rail = autoCollapsed ? !expandedNarrow : collapsed
  const toggle = useCallback(() => {
    if (autoCollapsed) setExpandedNarrow((v) => !v)
    else toggleSidebar()
  }, [autoCollapsed, toggleSidebar])

  // Either direction unmounts the control that was pressed, so its opposite
  // takes the focus. They are different elements, which is why this waits for
  // the render rather than moving focus first.
  const toggleControl = useRef<HTMLButtonElement>(null)
  const takeToggle = useRef(false)
  useEffect(() => {
    if (!takeToggle.current) return
    takeToggle.current = false
    toggleControl.current?.focus()
  }, [rail])
  const toggleAndFollow = useCallback(() => {
    takeToggle.current = true
    toggle()
  }, [toggle])

  const beginDrag = useDrag()

  const startResize = useCallback(
    (e: React.PointerEvent) => {
      e.preventDefault()
      const drag = beginDrag()
      const move = (ev: PointerEvent) => setSidebarWidth(ev.clientX)
      window.addEventListener('pointermove', move, { signal: drag.signal })
      for (const end of ['pointerup', 'pointercancel']) {
        window.addEventListener(end, () => drag.abort(), { signal: drag.signal })
      }
    },
    [beginDrag, setSidebarWidth],
  )

  const resizeKey = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === 'Enter') {
        e.preventDefault()
        toggleAndFollow()
        return
      }
      const next = splitterTarget(e.key, {
        value: width,
        min: minSidebarWidth,
        max: maxSidebarWidth,
        grow: 'ArrowRight',
        shrink: 'ArrowLeft',
      })
      if (next === null) return
      e.preventDefault()
      setSidebarWidth(next)
    },
    [setSidebarWidth, toggleAndFollow, width],
  )

  if (rail) {
    return (
      <aside className="flex w-10 shrink-0 flex-col items-center border-r bg-sidebar py-2">
        <Button
          ref={toggleControl}
          variant="ghost"
          size="icon"
          aria-label="Expand sidebar"
          onClick={toggleAndFollow}
        >
          <PanelLeftOpen />
        </Button>
      </aside>
    )
  }

  return (
    <aside
      id="sidebar"
      className="relative flex shrink-0 flex-col border-r bg-sidebar"
      style={{ width }}
      aria-label="Runs"
    >
      <WorkspaceSwitcher onCollapse={toggleAndFollow} controlRef={toggleControl} />
      <SidebarHeader />
      <RunTree />
      <NavSection />
      <div
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize sidebar"
        aria-controls="sidebar"
        aria-valuenow={Math.round(width)}
        aria-valuemin={minSidebarWidth}
        aria-valuemax={maxSidebarWidth}
        tabIndex={0}
        onPointerDown={startResize}
        onKeyDown={resizeKey}
        className={cn(
          focusRing,
          'absolute inset-y-0 -right-1 w-2 cursor-col-resize hover:bg-accent',
        )}
      />
    </aside>
  )
}

/**
 * The scoping control, above everything it scopes. A single workspace needs
 * no picker, so it renders as a plain label: the affordance appears only
 * when there is a choice to make.
 */
function WorkspaceSwitcher({
  onCollapse,
  controlRef,
}: {
  onCollapse: () => void
  controlRef: React.RefObject<HTMLButtonElement | null>
}) {
  const workspaces = useStore((s) => s.workspaces)
  const active = useStore((s) => s.activeWorkspace)
  const setActiveWorkspace = useStore((s) => s.setActiveWorkspace)
  const list = Object.values(workspaces)
  const current = workspaces[active]

  return (
    <div className="flex h-9 items-center gap-2 border-b px-2">
      <FolderGit2 className="size-4 shrink-0 text-muted-foreground" />
      {list.length > 1 ? (
        <select
          aria-label="Workspace"
          className={cn(
            focusRing,
            'min-w-0 flex-1 rounded-md border border-input bg-background px-2 py-1 text-sm',
          )}
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
        ref={controlRef}
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
  const openDialog = useStore((s) => s.openPaletteDialog)
  // The launch form is hosted app-wide, so the sidebar only has to ask for
  // it. A member who cannot start a run is not offered the way in.
  const launchable = canLaunch({ cap: useCapability(), role: useSelfRole() })
  return (
    <div className="flex flex-wrap items-center gap-1 border-b px-2 py-1.5">
      <span className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
        Runs
      </span>
      <AttentionBadge />
      <div className="ml-auto flex items-center gap-1">
        {launchable && (
          <Button
            variant="ghost"
            size="sm"
            title="Launch a run"
            onClick={() => openDialog('launch')}
          >
            <Rocket />
            New run
          </Button>
        )}
        <GroupByControl />
      </div>
    </div>
  )
}

function GroupByControl() {
  const groupBy = useStore((s) => s.groupBy)
  const setGroupBy = useStore((s) => s.setGroupBy)
  return (
    <div
      role="group"
      aria-label="Group runs by"
      className="flex items-center rounded-md border"
    >
      {([['status', 'Status'], ['member', 'Member']] as const).map(([mode, label]) => (
        <Button
          key={mode}
          variant="ghost"
          size="sm"
          aria-pressed={groupBy === mode}
          title={`Group runs by ${label.toLowerCase()}`}
          onClick={() => setGroupBy(mode)}
          className={cn(
            'rounded-none first:rounded-l-md last:rounded-r-md',
            groupBy === mode
              ? 'bg-accent font-medium text-accent-foreground'
              : 'text-muted-foreground',
          )}
        >
          {label}
        </Button>
      ))}
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

function approvalsLabel(label: string, waiting: number, error: string | null): string {
  if (error) return `${label}, queue could not be read`
  return waiting > 0 ? `${label}, ${waiting} waiting on a decision` : label
}

/** Board and All runs need no gate: both are views of the runs above them. */
function NavSection() {
  const cap = useCapability()
  const navigate = useStore((s) => s.navigate)
  const route = useStore((s) => s.route)
  const inbox = useStore((s) => s.inbox)
  const inboxError = useStore((s) => s.inboxError)
  const waiting = pendingApprovals(inbox).length
  const links: Surface[] = [
    { name: 'board', label: 'Board', Icon: LayoutGrid },
    { name: 'overview', label: 'All runs', Icon: List },
    ...surfaces(cap),
  ]
  return (
    <nav aria-label="Surfaces" className="shrink-0 border-t py-1">
      {links.map(({ name, label, Icon }) => (
        <button
          key={name}
          type="button"
          aria-label={
            name === 'approvals'
              ? approvalsLabel(label, waiting, inboxError)
              : undefined
          }
          aria-current={route.name === name ? 'page' : undefined}
          onClick={() => navigate(name)}
          className={cn(
            focusRing,
            'flex w-full items-center gap-2 border-l-2 border-transparent px-2 py-1 text-left text-sm hover:bg-accent/60',
            route.name === name && 'bg-accent border-primary',
          )}
        >
          <Icon className="size-3.5 text-muted-foreground" />
          {label}
          {name === 'approvals' && (waiting > 0 || inboxError !== null) && (
            <span
              aria-hidden
              title={inboxError ?? 'Requests waiting on a decision'}
              className={cn(
                'ml-auto rounded-full bg-state-needs-attention/15 px-1.5',
                'text-[11px] font-medium text-state-needs-attention',
              )}
            >
              {inboxError ? '?' : waiting}
            </span>
          )}
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
  const selected = isRunRoute(route, entry.run.id)
  return (
    <button
      type="button"
      aria-current={selected ? 'page' : undefined}
      onClick={() => navigate('terminal', { runId: entry.run.id })}
      style={{ borderLeftColor: entry.owner?.color }}
      className={cn(
        focusRing,
        // Full bleed inside a scroll container: an outline drawn outside the
        // row would be clipped at both edges.
        'focus-visible:-outline-offset-2',
        'flex w-full items-center gap-2 border-l-2 py-1 pr-2 pl-4 text-left text-xs hover:bg-accent/60',
        selected ? 'bg-accent' : 'border-l-transparent',
        unseen ? 'font-medium' : 'text-muted-foreground',
      )}
    >
      <StateDot
        state={entry.state}
        className={cn(entry.state === 'working' && 'state-pulse')}
      />
      <span className="truncate">{runLabel(entry.run)}</span>
      <span className="ml-auto shrink-0 text-muted-foreground">
        {entry.run.harness}
      </span>
    </button>
  )
}
