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
import { Chip, Tooltip } from '@/components/ui/heroui'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { canLaunch } from '@/lib/commands'
import { useDelayed, useDrag } from '@/lib/hooks'
import { inModal, keyboardBusy, splitterTarget } from '@/lib/keys'
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
const mobileQuery = '(max-width: 640px)'

export function Sidebar() {
  const collapsed = useStore((s) => s.sidebarCollapsed)
  const width = useStore((s) => s.sidebarWidth)
  const toggleSidebar = useStore((s) => s.toggleSidebar)
  const setSidebarWidth = useStore((s) => s.setSidebarWidth)
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
    const apply = (e: MediaQueryListEvent) => setMobile(e.matches)
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
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (
        !(e.metaKey || e.ctrlKey) ||
        e.altKey ||
        e.shiftKey ||
        e.key.toLowerCase() !== 'b' ||
        e.defaultPrevented ||
        keyboardBusy(e) ||
        inModal(e.target)
      )
        return
      e.preventDefault()
      toggleAndFollow()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [toggleAndFollow])

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
        maxWidth: mobile ? 'calc(100vw - 3rem)' : undefined,
      }}
      className={cn(
        'relative flex min-w-0 shrink-0 flex-col border-r border-border bg-sidebar',
        mobile && 'absolute inset-y-0 left-12 z-40 shadow-xl',
      )}
      aria-label="Runs"
    >
      <WorkspaceSwitcher onCollapse={toggleAndFollow} controlRef={toggleControl} />
      <SidebarHeader />
      <RunTree />
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
          'absolute inset-y-0 -right-1 w-2 cursor-col-resize hover:bg-toolbar-hover',
        )}
      />
    </aside>
  )

  return (
    <div className="relative flex h-full min-h-0 shrink-0">
      <ActivityRail
        sidebarCollapsed={rail}
        onToggleSidebar={toggleAndFollow}
        toggleControl={toggleControl}
      />
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

  return (
    <div className="flex h-[35px] shrink-0 items-center gap-1 border-b border-border px-2">
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
      <Button
        ref={controlRef}
        variant="ghost"
        size="icon"
        aria-label="Collapse sidebar"
        onClick={onCollapse}
        className="size-[26px] min-h-[26px] min-w-[26px] rounded-sm"
      >
        <PanelLeftClose className="size-4" />
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
    <div className="flex min-h-[35px] shrink-0 flex-wrap items-center gap-1 border-b border-border px-2 py-0.5">
      <span className="text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
        Runs
      </span>
      <AttentionBadge />
      <div className="ml-auto flex min-w-0 flex-1 flex-wrap items-center justify-end gap-1">
        {launchable && (
          <Tooltip>
            <Tooltip.Trigger<'button'>
              render={(triggerProps) => (
                <Button
                  {...triggerProps}
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    openDialog('launch')
                  }}
                  className="h-[26px] rounded-sm px-2 text-[12px]"
                >
                  <Rocket className="size-3.5" />
                  New run
                </Button>
              )}
            />
            <Tooltip.Content>Launch a run</Tooltip.Content>
          </Tooltip>
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
      className="flex shrink-0 items-center rounded-sm border border-border"
    >
      {([['status', 'Status'], ['member', 'Member']] as const).map(([mode, label]) => (
        <Tooltip key={mode}>
          <Tooltip.Trigger<'button'>
            render={(triggerProps) => (
              <Button
                {...triggerProps}
                variant="ghost"
                size="sm"
                aria-pressed={groupBy === mode}
                onClick={() => setGroupBy(mode)}
                className={cn(
                  'h-[26px] rounded-none px-2 text-[12px] first:rounded-l-sm last:rounded-r-sm',
                  groupBy === mode
                    ? 'bg-selection font-medium text-selection-foreground'
                    : 'text-muted-foreground',
                )}
              >
                {label}
              </Button>
            )}
          />
          <Tooltip.Content>Group runs by {label.toLowerCase()}</Tooltip.Content>
        </Tooltip>
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
      role="img"
      title="Runs waiting on a human"
      className="rounded-sm bg-state-needs-attention/15 px-1.5 text-[11px] font-medium text-state-needs-attention"
    >
      <Chip
        color="warning"
        variant="soft"
        size="sm"
        className="bg-state-needs-attention/15 text-state-needs-attention"
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

/** Existing routes live in a persistent 48px activity rail. */
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
    { name: 'board', label: 'Board', Icon: LayoutGrid },
    { name: 'overview', label: 'All runs', Icon: List },
    ...surfaceLinks.filter(
      ({ name }) => name !== 'onboarding' && name !== 'settings',
    ),
  ]
  const utilityLinks = surfaceLinks.filter(
    ({ name }) => name === 'onboarding' || name === 'settings',
  )

  const renderLinks = (links: Surface[]) =>
    links.map(({ name, label, Icon }) => {
      const current = route.name === name
      const accessibleLabel =
        name === 'approvals'
          ? approvalsLabel(label, waiting, inboxError)
          : label
      return (
        <Tooltip key={name}>
          <Tooltip.Trigger<'button'>
            render={(triggerProps) => (
              <button
                {...triggerProps}
                type="button"
                aria-label={accessibleLabel}
                aria-current={current ? 'page' : undefined}
                onClick={() => navigate(name)}
                className={cn(
                  focusRing,
                  'focus-visible:-outline-offset-2',
                  'relative flex h-12 min-h-12 w-12 shrink-0 items-center justify-center border-l-2 border-transparent text-muted-foreground transition-colors hover:bg-toolbar-hover hover:text-foreground',
                  current && 'border-primary text-foreground',
                )}
              >
                <Icon className="size-6" aria-hidden />
                <span className="sr-only">{label}</span>
                {name === 'approvals' && (waiting > 0 || inboxError !== null) && (
                  <Chip
                    color="warning"
                    variant="soft"
                    size="sm"
                    aria-hidden
                    render={(props) => (
                      <span {...props} title={inboxError ?? 'Requests waiting on a decision'} />
                    )}
                    className="absolute bottom-1 right-1 !h-4 !min-h-4 !min-w-4 !rounded-sm !px-0.5 !text-[10px] font-medium !leading-3 bg-state-needs-attention/15 text-state-needs-attention"
                  >
                    <Chip.Label>{inboxError ? '?' : waiting}</Chip.Label>
                  </Chip>
                )}
              </button>
            )}
          />
          <Tooltip.Content>{accessibleLabel}</Tooltip.Content>
        </Tooltip>
      )
    })

  return (
    <nav
      aria-label="Surfaces"
      className="flex h-full w-12 shrink-0 flex-col overflow-hidden border-r border-border bg-sidebar"
    >
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
                className={cn(
                  focusRing,
                  'focus-visible:-outline-offset-2',
                  'relative flex h-12 min-h-12 w-12 shrink-0 items-center justify-center border-l-2 border-transparent text-muted-foreground transition-colors hover:bg-toolbar-hover hover:text-foreground',
                )}
              >
                <PanelLeftOpen className="size-5" aria-hidden />
                <span className="sr-only">Expand sidebar</span>
              </button>
            )}
          />
          <Tooltip.Content>Expand sidebar</Tooltip.Content>
        </Tooltip>
      )}
      <div className="min-h-0 min-w-0 flex-1 overflow-y-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
        <div className="flex min-h-max flex-col py-1">{renderLinks(primaryLinks)}</div>
      </div>
      {utilityLinks.length > 0 && (
        <div className="flex shrink-0 flex-col py-1">
          {renderLinks(utilityLinks)}
        </div>
      )}
    </nav>
  )
}

function Group({ group }: { group: SidebarGroup }) {
  return (
    <section className="mb-1">
      <h2 className="px-3 py-0.5 text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
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
        'flex h-7 min-h-7 w-full items-center gap-2 border-l-2 py-0.5 pr-3 pl-5 text-left text-[13px] hover:bg-toolbar-hover',
        selected
          ? 'bg-selection font-medium text-selection-foreground'
          : unseen
            ? 'font-medium'
            : 'text-muted-foreground',
      )}
    >
      <StateDot
        state={entry.state}
        className={cn(entry.state === 'working' && 'state-pulse')}
      />
      <span className="truncate">{runLabel(entry.run)}</span>
      <span className={cn('ml-auto shrink-0', !selected && 'text-muted-foreground')}>
        {entry.run.harness}
      </span>
    </button>
  )
}
