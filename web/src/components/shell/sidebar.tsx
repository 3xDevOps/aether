import {
  ChevronDown,
  ChevronRight,
  FolderGit2,
  House,
  MoreHorizontal,
  PanelLeftClose,
  PanelLeftOpen,
  Plus,
  Users,
} from 'lucide-react'
import { Dialog as DialogPrimitive, DropdownMenu as DropdownMenuPrimitive } from 'radix-ui'
import { memo, useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { StateDot } from '@/components/state-dot'
import { RunInputIndicator } from '@/components/run-input-indicator'
import { Button } from '@/components/ui/button'
import { DialogOverlay, DialogPortal } from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Chip, Tooltip } from '@/components/ui/heroui'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { useDelayed, useDrag } from '@/lib/hooks'
import { isPress, shortcutLabel, useKeybindings } from '@/lib/keybindings'
import { splitterTarget } from '@/lib/keys'
import { runLabel, type PresentationState } from '@/lib/status'
import { surfaces, type Surface } from '@/lib/surfaces'
import { cn, focusRing } from '@/lib/utils'
import { isRunRoute } from '@/routes/terminal/tabs'
import { useStore } from '@/store'
import { pendingApprovals } from '@/store/approvals'
import {
  useCapability,
  useNeedsYouCount,
  useRun,
  useRunInput,
  useSidebarGroups,
} from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import type { SidebarGroup } from '@/store/selectors'
import { maxSidebarWidth, minSidebarWidth } from '@/store/ui'

/**
 * The desktop shell cannot open a window narrower than 960px, so this matches
 * at the smallest window there is: a sidebar at its default width plus three
 * board columns does not fit in it, and the rail is what keeps the board
 * readable.
 */
const narrowQuery = '(max-width: 1000px)'
const mobileQuery = '(max-width: 640px)'

export function Sidebar() {
  const collapsed = useStore((s) => s.sidebarCollapsed)
  const width = useStore((s) => Math.max(minSidebarWidth, s.sidebarWidth))
  const toggleSidebar = useStore((s) => s.toggleSidebar)
  const setSidebarWidth = useStore((s) => s.setSidebarWidth)
  const drawerOpen = useStore((s) => s.sidebarDrawerOpen)
  const setDrawerOpen = useStore((s) => s.setSidebarDrawerOpen)
  const drawerOpener = useRef<HTMLElement | null>(null)
  const [autoCollapsed, setAutoCollapsed] = useState(
    () => window.matchMedia?.(narrowQuery).matches ?? false,
  )
  const [mobile, setMobile] = useState(
    () => window.matchMedia?.(mobileQuery).matches ?? false,
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

  useEffect(() => {
    const media = window.matchMedia?.(mobileQuery)
    if (!media) return
    const apply = (e: MediaQueryListEvent) => {
      setMobile(e.matches)
      setExpandedNarrow(false)
      setDrawerOpen(false)
    }
    media.addEventListener('change', apply)
    return () => media.removeEventListener('change', apply)
  }, [setDrawerOpen])

  useEffect(() => () => setDrawerOpen(false), [setDrawerOpen])

  const rail = !mobile && (autoCollapsed ? !expandedNarrow : collapsed)

  const toggle = useCallback(() => {
    if (mobile) setDrawerOpen(!useStore.getState().sidebarDrawerOpen)
    else if (autoCollapsed) setExpandedNarrow((v) => !v)
    else toggleSidebar()
  }, [mobile, autoCollapsed, setDrawerOpen, toggleSidebar])

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
    takeToggle.current = !mobile
    toggle()
  }, [mobile, toggle])
  useKeybindings('global', {
    sidebar: (e) => {
      e.preventDefault()
      toggleAndFollow()
    },
  })

  // Every navigation out of the drawer is a navigation into the view the
  // drawer covers, so the route itself closes it: a run row and a rail link
  // both land here without either knowing about the drawer. Only a change of
  // route may close it, which is why the last one is held rather than
  // compared by the dependency list - opening the drawer re-runs this effect
  // and must not close it again.
  const route = useStore((s) => s.route)
  const lastRoute = useRef(route)
  useEffect(() => {
    if (lastRoute.current === route) return
    lastRoute.current = route
    if (!drawerOpen) return
    setDrawerOpen(false)
  }, [drawerOpen, route, setDrawerOpen])

  const beginDrag = useDrag()

  const startResize = useCallback(
    (e: React.PointerEvent) => {
      e.preventDefault()
      const startX = e.clientX
      const startWidth = width
      const drag = beginDrag()
      const move = (ev: PointerEvent) =>
        setSidebarWidth(startWidth + ev.clientX - startX)
      window.addEventListener('pointermove', move, { signal: drag.signal })
      for (const end of ['pointerup', 'pointercancel']) {
        window.addEventListener(end, () => drag.abort(), { signal: drag.signal })
      }
    },
    [beginDrag, setSidebarWidth, width],
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

  const sidebar = rail ? null : (
    <aside
      id="sidebar"
      style={{
        width,
        maxWidth: mobile ? 'calc(100vw - 3rem - env(safe-area-inset-left))' : undefined,
      }}
      className="relative flex min-w-0 shrink-0 flex-col border-r border-border bg-sidebar"
      aria-label="Runs"
    >
      <WorkspaceSwitcher onCollapse={toggleAndFollow} controlRef={toggleControl} />
      <SidebarHeader />
      <RunTree />
      {/* The drawer is sized by the viewport, not by a splitter. */}
      {!mobile && (
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
            // Without `touch-none` the browser claims a touch drag as a pan
            // and cancels the pointer stream this listens to. The coarse hit
            // area is 24px centred on the edge, and it needs the z-index to
            // win the half of itself that overhangs the pane beside it.
            'absolute inset-y-0 -right-1 z-10 w-2 cursor-col-resize touch-none hover:bg-toolbar-hover coarse:-right-3 coarse:w-6',
          )}
        />
      )}
    </aside>
  )

  const nav = (
    <ActivityRail
      sidebarCollapsed={rail}
      onToggleSidebar={toggleAndFollow}
      toggleControl={toggleControl}
    />
  )

  // The phone rail exists only inside the modal: the center owns the full
  // available width while it is closed and never reflows behind the scrim.
  if (mobile) {
    return (
        <DialogPrimitive.Root open={drawerOpen} onOpenChange={setDrawerOpen}>
          <DialogPortal>
            <DialogOverlay />
            <DialogPrimitive.Content
              id="sidebar-drawer"
              aria-describedby={undefined}
              onOpenAutoFocus={() => {
                drawerOpener.current =
                  document.activeElement instanceof HTMLElement
                    ? document.activeElement
                    : null
              }}
              onCloseAutoFocus={(event) => {
                event.preventDefault()
                const opener = drawerOpener.current
                if (opener?.isConnected && opener !== document.body) opener.focus()
                else document.getElementById('sidebar-drawer-trigger')?.focus()
              }}
              // A dialog stands the shell's global keys down inside itself,
              // and Mod+B is the pair of the key that opened this one, so the
              // drawer answers it here.
              onKeyDown={(event) => {
                if (!isPress(event.nativeEvent, 'sidebar')) return
                event.preventDefault()
                toggleAndFollow()
              }}
              // viewport-fit=cover puts this under the notch and the home
              // indicator, so it paints to the edges and insets what it
              // holds, the way the title bar and status bar do.
              className="fixed inset-y-0 left-0 z-50 flex max-w-full bg-sidebar pt-[var(--safe-top)] pb-[env(safe-area-inset-bottom)] pl-[env(safe-area-inset-left)] shadow-xl outline-none data-[state=open]:animate-in data-[state=open]:slide-in-from-left motion-reduce:animate-none"
            >
              <DialogPrimitive.Title className="sr-only">Runs</DialogPrimitive.Title>
              {nav}
              {sidebar}
            </DialogPrimitive.Content>
          </DialogPortal>
        </DialogPrimitive.Root>
    )
  }

  return (
    <div className="relative flex h-full min-h-0 shrink-0">
      {nav}
      {sidebar}
    </div>
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
  const navigate = useStore((s) => s.navigate)
  const caps = useCapability()

  return (
    <div className="flex h-[var(--title-bar-height)] shrink-0 items-center gap-1 border-b border-border px-2">
      <FolderGit2 className="size-4 shrink-0 text-muted-foreground" />
      {list.length > 1 ? (
        <Select value={active} onValueChange={setActiveWorkspace}>
          <SelectTrigger aria-label="Workspace" className="min-w-0 flex-1">
            <SelectValue placeholder="Choose a workspace" />
          </SelectTrigger>
          <SelectContent>
            {list.map((workspace) => (
              <SelectItem key={workspace.id} value={workspace.id}>
                {workspace.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      ) : (
        <span className="flex min-w-0 flex-1 flex-col items-start leading-tight">
          <span className="truncate text-[13px] font-semibold">
            {current?.name ?? 'No workspace'}
          </span>
          {current && (
            <span className="truncate text-[11px] text-muted-foreground">
              {current.base_branch}
            </span>
          )}
        </span>
      )}
      {caps.hasMethod('workspace.list') && <Button variant="ghost" size="icon" aria-label="Add or manage workspaces" title="Add or manage workspaces" onClick={() => navigate('workspaces')}><Plus className="size-4" /></Button>}
      <Button
        ref={controlRef}
        variant="ghost"
        size="icon"
        aria-label="Collapse sidebar"
        title={`Collapse sidebar · ${shortcutLabel('sidebar')}`}
        onClick={onCollapse}
        className="size-[26px] min-h-[26px] min-w-[26px] rounded-sm coarse:size-11 coarse:min-h-11 coarse:min-w-11"
      >
        <PanelLeftClose className="size-4" />
      </Button>
    </div>
  )
}

function SidebarHeader() {
  return (
    <div className="flex min-h-[var(--title-bar-height)] shrink-0 items-center gap-1 overflow-x-auto border-b border-border px-2 py-0.5">
      <span className="shrink-0 text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
        Runs
      </span>
      <NeedsYouBadge />
      <div className="ml-auto flex shrink-0 items-center gap-1">
        <MineToggle />
      </div>
    </div>
  )
}

function MineToggle() {
  const mineOnly = useStore((s) => s.mineOnly)
  const setMineOnly = useStore((s) => s.setMineOnly)
  return (
    <Tooltip>
      <Tooltip.Trigger<'button'>
        render={(triggerProps) => (
          <Button
            {...triggerProps}
            variant="ghost"
            size="sm"
            aria-pressed={mineOnly}
            onClick={() => setMineOnly(!mineOnly)}
            className={cn(
              'h-[26px] rounded-sm border border-border px-2 text-[12px] coarse:h-11',
              mineOnly ? 'bg-selection font-medium text-selection-foreground' : 'text-muted-foreground',
            )}
          >
            Mine
          </Button>
        )}
      />
      <Tooltip.Content>Show only your runs under Working and Finished</Tooltip.Content>
    </Tooltip>
  )
}

function NeedsYouBadge() {
  const count = useNeedsYouCount()
  if (count === 0) return null
  return (
    <span
      aria-label={`${count} ${count === 1 ? 'run needs' : 'runs need'} you`}
      title={`${count} ${count === 1 ? 'run needs' : 'runs need'} you`}
      role="img"
      className="rounded-sm bg-state-needs-you/15 px-1.5 text-[11px] font-medium text-state-needs-you"
    >
      <Chip
        color="warning"
        variant="soft"
        size="sm"
        className="bg-state-needs-you/15 text-state-needs-you"
      >
        <Chip.Label>{count}</Chip.Label>
      </Chip>
    </span>
  )
}

function RunTree() {
  const groups = useSidebarGroups()
  const hydrated = useStore((s) => s.hydrated)
  const error = useStore((s) => s.hydrationError)
  const dead = useStore((s) => s.streamDead)
  const [expandedByGroup, setExpandedByGroup] = useState<Record<string, boolean>>({})
  const unreachable = error !== null
  const loading = useDelayed(!hydrated && !unreachable && groups.length === 0)

  const toggleGroup = useCallback((key: string, initiallyExpanded: boolean) => {
    setExpandedByGroup((current) => ({
      ...current,
      [key]: !(current[key] ?? initiallyExpanded),
    }))
  }, [])

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
      {groups.map((group) => {
        const initiallyExpanded = group.key !== 'finished'
        const expanded = expandedByGroup[group.key] ?? initiallyExpanded
        return (
          <Group
            key={group.key}
            group={group}
            expanded={expanded}
            onToggle={() => toggleGroup(group.key, initiallyExpanded)}
            regionId={`sidebar-run-group-${group.key}`}
          />
        )
      })}
    </div>
  )
}

function approvalsLabel(label: string, waiting: number, error: string | null): string {
  if (error) return `${label}, ${error}`
  return waiting > 0 ? `${label}, ${waiting} waiting on a decision` : label
}

/** Work and workspace navigation stay visible or explicitly reachable in More. */
export function ActivityRail({
  sidebarCollapsed,
  onToggleSidebar,
  toggleControl,
}: {
  sidebarCollapsed: boolean
  onToggleSidebar: () => void
  toggleControl: React.RefObject<HTMLButtonElement | null>
}) {
  const cap = useCapability()
  const navigate = useStore((s) => s.navigate)
  const route = useStore((s) => s.route)
  const inbox = useStore((s) => s.inbox)
  const inboxError = useStore((s) => s.inboxError)
  const waiting = pendingApprovals(inbox).length
  const surfaceLinks = surfaces(cap)
  const primaryLinks: Surface[] = [
    { name: 'board', label: 'Board', Icon: House, group: 'Work' },
    ...surfaceLinks.filter(({ group }) => group === 'Work' || group === 'Workspace'),
  ]
  const adminLinks = surfaceLinks.filter(({ group }) => group === 'Admin')
  const settingsLinks = surfaceLinks.filter(({ group }) => group === 'Settings')
  const railRef = useRef<HTMLElement>(null)
  const [height, setHeight] = useState<number | null>(null)

  useLayoutEffect(() => {
    const element = railRef.current
    if (!element) return
    const measure = () => {
      const next = element.getBoundingClientRect().height
      if (next > 0) setHeight(next)
    }
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(element)
    return () => observer.disconnect()
  }, [])

  // Rows are 48px and each nonempty group has a 20px heading. Measure the
  // containing rail, not the viewport: update banners also consume shell space.
  const available = height === null
    ? Infinity
    : height - (sidebarCollapsed ? 48 : 0) - 48 - (adminLinks.length ? 48 : 0) - 1
  const groupHeight = (count: number) => {
    let groups = 0
    for (let i = 0; i < count; i++) {
      if (i === 0 || primaryLinks[i].group !== primaryLinks[i - 1].group) groups++
    }
    return count * 48 + groups * 20
  }
  let visibleCount = primaryLinks.length
  if (groupHeight(visibleCount) > available) {
    while (visibleCount > 0 && groupHeight(visibleCount) + 48 > available) visibleCount--
  }
  const visibleLinks = primaryLinks.slice(0, visibleCount)
  const overflowLinks = primaryLinks.slice(visibleCount)
  const railButton = cn(
    focusRing,
    'focus-visible:-outline-offset-2 relative flex h-12 min-h-12 w-12 shrink-0 items-center justify-center border-l-2 border-transparent text-muted-foreground transition-colors hover:bg-toolbar-hover hover:text-foreground',
  )
  const labelFor = ({ name, label }: Surface) =>
    name === 'approvals' ? approvalsLabel(label, waiting, inboxError) : label
  const approvalBadge = (waiting > 0 || inboxError !== null) && (
    <span aria-hidden className="absolute bottom-1 right-1 flex">
      <Chip
        color="warning"
        variant="soft"
        size="sm"
        className="!h-4 !min-h-4 !min-w-4 !rounded-sm !px-0.5 !text-[10px] font-medium !leading-3 bg-state-needs-attention/15 text-state-needs-attention"
      >
        <Chip.Label>{inboxError ? '?' : waiting}</Chip.Label>
      </Chip>
    </span>
  )

  const renderLink = (surface: Surface) => {
    const { name, label, Icon } = surface
    return (
      <Tooltip key={name}>
        <Tooltip.Trigger<'button'>
          render={(triggerProps) => (
            <button
              {...triggerProps}
              type="button"
              aria-label={labelFor(surface)}
              aria-current={route.name === name ? 'page' : undefined}
              onClick={() => navigate(name)}
              className={cn(railButton, route.name === name && 'border-primary text-foreground')}
            >
              <Icon className="size-6" aria-hidden />
              <span className="sr-only">{label}</span>
              {name === 'approvals' && approvalBadge}
            </button>
          )}
        />
        <Tooltip.Content>{labelFor(surface)}</Tooltip.Content>
      </Tooltip>
    )
  }

  const renderMenu = (label: string, links: Surface[], Icon: Surface['Icon']) => {
    const current = links.find(({ name }) => name === route.name)
    const hasApprovals = links.some(({ name }) => name === 'approvals')
    const accessibleLabel = [
      label,
      current?.label,
      hasApprovals && (waiting > 0 || inboxError) ? approvalsLabel('Approvals', waiting, inboxError) : null,
    ].filter(Boolean).join(', ')
    return (
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            aria-label={accessibleLabel}
            className={cn(railButton, 'flex-col gap-0.5', current && 'border-primary text-foreground')}
          >
            <Icon className="size-5" aria-hidden />
            <span className="text-[10px] leading-3">{label}</span>
            {hasApprovals && approvalBadge}
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent side="right" align="start" aria-label={label}>
          {(['Work', 'Workspace', 'Admin'] as const).map((group) => {
            const entries = links.filter((surface) => surface.group === group)
            if (!entries.length) return null
            return (
              <DropdownMenuPrimitive.Group key={group} aria-label={group}>
                <div aria-hidden className="px-2 py-1 text-[11px] text-muted-foreground">{group}</div>
                {entries.map((surface) => (
                  <DropdownMenuItem
                    key={surface.name}
                    aria-label={labelFor(surface)}
                    aria-current={route.name === surface.name ? 'page' : undefined}
                    onSelect={() => navigate(surface.name)}
                  >
                    <surface.Icon aria-hidden />
                    {surface.label}
                    {surface.name === 'approvals' && (waiting > 0 || inboxError !== null) && (
                      <span aria-hidden className="ml-auto text-state-needs-attention">{inboxError ? '?' : waiting}</span>
                    )}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuPrimitive.Group>
            )
          })}
        </DropdownMenuContent>
      </DropdownMenu>
    )
  }

  return (
    <nav ref={railRef} aria-label="Surfaces" className="flex h-full w-12 shrink-0 flex-col border-r border-border bg-sidebar">
      {sidebarCollapsed && (
        <Tooltip>
          <Tooltip.Trigger<'button'>
            render={(triggerProps) => (
              <button
                {...triggerProps}
                ref={toggleControl}
                type="button"
                aria-label="Expand sidebar"
                onClick={onToggleSidebar}
                className={railButton}
              >
                <PanelLeftOpen className="size-5" aria-hidden />
              </button>
            )}
          />
          <Tooltip.Content>Expand sidebar · {shortcutLabel('sidebar')}</Tooltip.Content>
        </Tooltip>
      )}
      <div className="min-h-0 flex-1">
        {(['Work', 'Workspace'] as const).map((group) => {
          const links = visibleLinks.filter((surface) => surface.group === group)
          if (!links.length) return null
          return (
            <div key={group} role="group" aria-label={group}>
              <div aria-hidden className="flex h-5 items-center justify-center border-t border-border text-[8px] leading-none text-muted-foreground">
                {group}
              </div>
              {links.map(renderLink)}
            </div>
          )
        })}
        {overflowLinks.length > 0 && renderMenu('More', overflowLinks, MoreHorizontal)}
      </div>
      <div className="flex shrink-0 flex-col border-t border-border">
        {adminLinks.length > 0 && renderMenu('Admin', adminLinks, Users)}
        {settingsLinks.map(renderLink)}
      </div>
    </nav>
  )
}

function Group({
  group,
  expanded,
  onToggle,
  regionId,
}: {
  group: SidebarGroup
  expanded: boolean
  onToggle: () => void
  regionId: string
}) {
  return (
    <section className="mb-1">
      <h2>
        <button
          type="button"
          aria-expanded={expanded}
          aria-controls={regionId}
          onClick={onToggle}
          className={cn(
            focusRing,
            'flex w-full items-center gap-1 px-3 py-0.5 text-left text-[11px] font-semibold tracking-wide text-muted-foreground uppercase hover:bg-toolbar-hover',
          )}
        >
          {expanded ? (
            <ChevronDown className="size-3.5 shrink-0" />
          ) : (
            <ChevronRight className="size-3.5 shrink-0" />
          )}
          <span className="truncate">{group.label}</span>
          <span className="ml-auto shrink-0 normal-case">
            {group.count}
          </span>
        </button>
      </h2>
      <ul id={regionId} hidden={!expanded}>
        {expanded && group.runs.map((run) => (
          <li key={run.run.id}>
            <RunRow runID={run.run.id} state={run.state} reason={run.reason} workspaceName={run.workspaceName} />
            {run.children.length > 0 && (
              <ul aria-label={`Workers of ${runLabel(run.run)}`}>
                {run.children.map((child, index) => (
                  <li key={child.run.id}>
                    <RunRow runID={child.run.id} state={child.state} reason={child.reason} workspaceName={child.workspaceName} branch={index === run.children.length - 1 ? 'last' : 'middle'} />
                  </li>
                ))}
              </ul>
            )}
          </li>
        ))}
      </ul>
    </section>
  )
}

/** Subscribes to its own run, so an event about another run leaves it alone. */
const RunRow = memo(function RunRow({ runID, ...shown }: {
  runID: string
  state: PresentationState
  reason: string
  workspaceName?: string
  branch?: 'middle' | 'last'
}) {
  const run = useRun(runID)
  return run ? <RunRowButton run={run} {...shown} /> : null
})

function RunRowButton({ run, state, reason, workspaceName, branch }: {
  run: RunRecord
  state: PresentationState
  reason: string
  workspaceName?: string
  branch?: 'middle' | 'last'
}) {
  const navigate = useStore((s) => s.navigate)
  const selected = useStore((s) => isRunRoute(s.route, run.id))
  const ownerColor = useStore((s) => s.members[run.member_id]?.color)
  const label = runLabel(run)
  const input = useRunInput(run)
  const role = run.mission_role === 'integrator'
    ? 'Integrator'
    : run.mission_role === 'worker' ? 'Worker' : undefined
  const description = [workspaceName, label, role, run.harness, reason, input.count > 0 && `Requests: ${input.summary}`].filter(Boolean).join(' · ')
  return (
    <button
      type="button"
      aria-current={selected ? 'page' : undefined}
      aria-label={description}
      title={description}
      onClick={() => navigate('terminal', { runId: run.id })}
      style={{ borderLeftColor: ownerColor }}
      className={cn(
        focusRing,
        // Full bleed inside a scroll container: an outline drawn outside the
        // row would be clipped at both edges.
        'focus-visible:-outline-offset-2',
        'relative flex h-7 min-h-7 w-full items-center gap-2 border-l-2 py-0.5 pr-3 text-left text-[13px] hover:bg-toolbar-hover coarse:h-11 coarse:min-h-11',
        branch ? 'pl-11' : 'pl-5',
        selected
          ? 'bg-selection font-medium text-selection-foreground'
          : state === 'needs-you'
            ? 'font-medium'
            : 'text-muted-foreground',
      )}
    >
      {branch && (
        <span aria-hidden className="pointer-events-none absolute inset-y-0 left-6 w-3 text-muted-foreground/40">
          <span className={cn('absolute top-0 left-0 border-l border-current', branch === 'last' ? 'h-1/2' : 'h-full')} />
          <span className="absolute top-1/2 left-0 w-3 border-t border-current" />
        </span>
      )}
      <StateDot
        state={state}
        className={cn(state === 'working' && 'state-pulse')}
      />
      {workspaceName && <span className="shrink-0 text-muted-foreground">{workspaceName} ·</span>}
      <span className="min-w-0 truncate">{label}</span>
      <RunInputIndicator run={run} compact />
      {role && !branch && (
        <span className={cn(
          'shrink-0 rounded-sm border px-1 text-[10px] font-medium leading-4',
          selected ? 'border-current/40' : 'border-primary/40 bg-primary/10 text-foreground',
        )}>
          {role}
        </span>
      )}
      <span className={cn('ml-auto shrink-0', !selected && 'text-muted-foreground')}>
        {run.harness}
      </span>
    </button>
  )
}
