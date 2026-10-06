import { useCallback, useEffect, useRef } from 'react'
import { PanelLeft, Plus, Search } from '@/components/icons'
import { focusView } from '@/components/shell/center-view'
import { SidebarFooter, UpdateNotice } from '@/components/shell/sidebar-footer'
import { needsYouRoute, runRowSelector, SidebarRuns } from '@/components/shell/sidebar-runs'
import { WorkspaceSwitcher } from '@/components/shell/workspace-switcher'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { ListRow } from '@/components/ui/list-row'
import { Separator } from '@/components/ui/separator'
import { useIsMobile } from '@/lib/breakpoints'
import { canLaunch } from '@/lib/commands'
import { useDrag } from '@/lib/hooks'
import { isPress, shortcutLabel, useKeybindings } from '@/lib/keybindings'
import { splitterTarget } from '@/lib/keys'
import { surfaces } from '@/lib/surfaces'
import { cn, focusRing } from '@/lib/utils'
import { isRunRoute } from '@/routes/terminal/tabs'
import { useStore } from '@/store'
import { useCapability, useIsAdmin, useSelfRole } from '@/store/hooks'
import { sidebarGroups, stateContextOf } from '@/store/selectors'
import { maxSidebarWidth, minSidebarWidth } from '@/store/ui'

function moveRowFocus(step: 1 | -1) {
  const rows = [...document.querySelectorAll<HTMLElement>(runRowSelector)]
  if (rows.length === 0) return
  const focused = rows.indexOf(document.activeElement as HTMLElement)
  const index = focused === -1 ? rows.findIndex((row) => row.getAttribute('aria-current') === 'page') : focused
  const next = index === -1 ? (step === 1 ? 0 : rows.length - 1) : Math.min(rows.length - 1, Math.max(0, index + step))
  rows[next]?.focus()
}

function openNextNeedsYou() {
  const s = useStore.getState()
  const ctx = stateContextOf(s, Date.now())
  const waiting = sidebarGroups({ workspace: s.activeWorkspace, mineOnly: s.mineOnly, ctx })
    .find((group) => group.key === 'needs-you')
    ?.runs.flatMap((tree) => [tree, ...tree.children])
    .filter((row) => row.state === 'needs-you') ?? []
  if (waiting.length === 0) return
  const current = waiting.findIndex((row) => {
    const route = needsYouRoute(row.run, ctx)
    return route.name === 'missions'
      ? s.route.name === 'missions' && s.route.params.missionId === route.params.missionId
      : isRunRoute(s.route, row.run.id)
  })
  const next = waiting[(current + 1) % waiting.length]!
  const route = needsYouRoute(next.run, ctx)
  s.navigate(route.name, route.params)
}

export function Sidebar() {
  const mobile = useIsMobile()
  const collapsed = useStore((s) => s.sidebarCollapsed)
  const toggleSidebar = useStore((s) => s.toggleSidebar)
  const drawerOpen = useStore((s) => s.sidebarDrawerOpen)
  const setDrawerOpen = useStore((s) => s.setSidebarDrawerOpen)
  const toggle = useCallback(() => {
    if (mobile) setDrawerOpen(!useStore.getState().sidebarDrawerOpen)
    else toggleSidebar()
  }, [mobile, setDrawerOpen, toggleSidebar])

  // Hiding or showing the sidebar removes the control that did it, so its
  // counterpart takes the focus that fell to the body.
  const shown = useRef(collapsed)
  useEffect(() => {
    if (shown.current === collapsed) return
    shown.current = collapsed
    if (document.activeElement && document.activeElement !== document.body) return
    const target = collapsed
      ? document.querySelector<HTMLElement>('main [aria-label="Open sidebar"]')
      : document.getElementById('sidebar-hide')
    target?.focus()
  }, [collapsed])

  useKeybindings('global', {
    sidebar: (event) => {
      event.preventDefault()
      toggle()
    },
    'row-next': () => moveRowFocus(1),
    'row-previous': () => moveRowFocus(-1),
    'next-needs-you': openNextNeedsYou,
  })

  useEffect(() => {
    if (!mobile) setDrawerOpen(false)
  }, [mobile, setDrawerOpen])

  // Any navigation lands in the view the drawer covers, so the route closes it.
  const route = useStore((s) => s.route)
  const lastRoute = useRef(route)
  const closedByRoute = useRef(false)
  const opener = useRef<HTMLElement | null>(null)
  useEffect(() => {
    if (lastRoute.current === route) return
    lastRoute.current = route
    if (!useStore.getState().sidebarDrawerOpen) return
    closedByRoute.current = true
    setDrawerOpen(false)
  }, [route, setDrawerOpen])

  if (mobile) {
    return (
      <Dialog open={drawerOpen} onOpenChange={setDrawerOpen}>
        <DialogContent
          id="sidebar-drawer"
          variant="side"
          side="left"
          showCloseButton={false}
          aria-describedby={undefined}
          className="w-[min(20rem,calc(100vw-3rem))]"
          onOpenAutoFocus={() => {
            opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
          }}
          onCloseAutoFocus={(event) => {
            event.preventDefault()
            if (closedByRoute.current) focusView()
            else (opener.current?.isConnected ? opener.current : document.getElementById('sidebar-drawer-trigger'))?.focus()
            closedByRoute.current = false
          }}
          // A dialog stands the global keys down inside itself, so the
          // drawer answers the key that toggles it.
          onKeyDown={(event) => {
            if (!isPress(event.nativeEvent, 'sidebar')) return
            event.preventDefault()
            setDrawerOpen(false)
          }}
        >
          <DialogTitle className="sr-only">Aether</DialogTitle>
          <SidebarContent onHide={() => setDrawerOpen(false)} />
        </DialogContent>
      </Dialog>
    )
  }

  if (collapsed) return null
  return <DesktopSidebar onHide={toggle} />
}

function DesktopSidebar({ onHide }: { onHide: () => void }) {
  const width = useStore((s) => Math.min(maxSidebarWidth, Math.max(minSidebarWidth, s.sidebarWidth)))
  const setSidebarWidth = useStore((s) => s.setSidebarWidth)
  const beginDrag = useDrag()

  const startResize = (event: React.PointerEvent) => {
    event.preventDefault()
    const startX = event.clientX
    const drag = beginDrag()
    const move = (ev: PointerEvent) => setSidebarWidth(width + ev.clientX - startX)
    window.addEventListener('pointermove', move, { signal: drag.signal })
    for (const end of ['pointerup', 'pointercancel']) {
      window.addEventListener(end, () => drag.abort(), { signal: drag.signal })
    }
  }

  const resizeKey = (event: React.KeyboardEvent) => {
    if (event.key === 'Enter') {
      event.preventDefault()
      onHide()
      return
    }
    const next = splitterTarget(event.key, {
      value: width,
      min: minSidebarWidth,
      max: maxSidebarWidth,
      grow: 'ArrowRight',
      shrink: 'ArrowLeft',
    })
    if (next === null) return
    event.preventDefault()
    setSidebarWidth(next)
  }

  return (
    <div
      id="sidebar"
      style={{ width }}
      className="relative flex h-full shrink-0 flex-col border-r border-seam bg-chrome pl-[env(safe-area-inset-left)]"
    >
      <SidebarContent onHide={onHide} />
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
          // and cancels the pointer stream this listens to.
          'absolute inset-y-0 -right-1 z-10 w-2 cursor-col-resize touch-none hover:bg-hover-chrome coarse:-right-3 coarse:w-6',
        )}
      />
    </div>
  )
}

function SidebarContent({ onHide }: { onHide: () => void }) {
  const mobile = useIsMobile()
  const cap = useCapability()
  const admin = useIsAdmin()
  const role = useSelfRole()
  const togglePalette = useStore((s) => s.togglePalette)
  const setDrawerOpen = useStore((s) => s.setSidebarDrawerOpen)
  const openDialog = useStore((s) => s.openPaletteDialog)
  const all = surfaces(cap, admin)
  const nav = all.filter((surface) => surface.place === 'nav')
  const lower = all.filter((surface) => surface.place === 'admin')

  return (
    <nav aria-label="Aether" className="flex min-h-0 flex-1 flex-col">
      <div className="flex h-11 shrink-0 items-center gap-1 px-2">
        <WorkspaceSwitcher />
        <Button
          variant="ghost"
          size="icon"
          label="Search"
          hint={`Search · ${shortcutLabel('palette')}`}
          onClick={() => {
            setDrawerOpen(false)
            togglePalette(true)
          }}
        >
          <Search />
        </Button>
        <Button
          id="sidebar-hide"
          variant="ghost"
          size="icon"
          label={mobile ? 'Close sidebar' : 'Hide sidebar'}
          hint={`${mobile ? 'Close' : 'Hide'} sidebar · ${shortcutLabel('sidebar')}`}
          onClick={onHide}
        >
          <PanelLeft />
        </Button>
      </div>
      {canLaunch({ cap, role }) && (
        <div className="shrink-0 px-2 pb-1">
          <Button
            hint={`New run · ${shortcutLabel('launch')}`}
            onClick={() => {
              setDrawerOpen(false)
              openDialog('launch')
            }}
            className="w-full"
          >
            <Plus />
            New run
          </Button>
        </div>
      )}
      <SidebarRuns />
      <div className="shrink-0 border-t border-seam px-2 py-2">
        <NavRows surfaces={nav} />
        {lower.length > 0 && (
          <>
            <Separator className="my-1" />
            <NavRows surfaces={lower} />
          </>
        )}
      </div>
      <UpdateNotice />
      <SidebarFooter />
    </nav>
  )
}

function NavRows({ surfaces: list }: { surfaces: ReturnType<typeof surfaces> }) {
  const current = useStore((s) => s.route.name)
  const navigate = useStore((s) => s.navigate)
  return (
    <ul>
      {list.map(({ name, label, Icon }) => (
        <li key={name}>
          <ListRow
            selected={current === name}
            aria-current={current === name ? 'page' : undefined}
            leading={<Icon />}
            onClick={() => navigate(name)}
          >
            {label}
          </ListRow>
        </li>
      ))}
    </ul>
  )
}
