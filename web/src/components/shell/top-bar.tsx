import { useLayoutEffect, useMemo, useRef, useState } from 'react'
import { Ellipsis, PanelLeft, Plus, Search } from '@/components/icons'
import { focusView } from '@/components/shell/center-view'
import { ConnectionLine } from '@/components/shell/connection'
import { Button } from '@/components/ui/button'
import { ListRow } from '@/components/ui/list-row'
import { Menu, MenuContent, MenuItem, MenuTrigger } from '@/components/ui/menu'
import { useIsMobile } from '@/lib/breakpoints'
import { canLaunch, launchBlocked } from '@/lib/commands'
import { runLabel } from '@/lib/status'
import { surfaces } from '@/lib/surfaces'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import { useCapability, useNeedsYouCount, useSelfRole } from '@/store/hooks'

const titles: Record<string, string> = {
  overview: 'All workspaces',
  missions: 'Swarms',
}

export function TopBar() {
  const mobile = useIsMobile()
  const header = useRef<HTMLElement>(null)
  useLayoutEffect(() => {
    const element = header.current
    if (!element) return
    const root = document.documentElement
    const measure = () => root.style.setProperty('--app-header-bottom', `${element.getBoundingClientRect().bottom}px`)
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(element)
    return () => {
      observer.disconnect()
      root.style.removeProperty('--app-header-bottom')
    }
  }, [])

  return (
    <header ref={header} className="shrink-0 border-b border-seam bg-chrome pt-[var(--safe-top)] pr-[env(safe-area-inset-right)] pl-[env(safe-area-inset-left)]">
      <div className="flex min-h-11 flex-wrap items-center gap-x-2 px-2 coarse:min-h-12">
        <div className="flex shrink-0 items-center gap-1.5">
          <img src="/aether-mark.png" alt="" width={24} height={24} />
          <span className="text-title">Aether</span>
        </div>
        <HeaderNavigation />
      </div>
      {mobile && <PhoneToolbar />}
    </header>
  )
}

function HeaderNavigation() {
  const cap = useCapability()
  const list = useMemo(() => surfaces(cap).filter((surface) => surface.place === 'nav'), [cap])
  const pinned = list.filter(({ name }) => name === 'board' || name === 'missions').length
  const current = useStore((s) => s.route.name)
  const navigate = useStore((s) => s.navigate)
  const row = useRef<HTMLUListElement>(null)
  const trigger = useRef<HTMLButtonElement>(null)
  const focusAfterResize = useRef<HTMLElement | null>(null)
  const navigated = useRef(false)
  const [open, setOpen] = useState(false)
  const [layout, setLayout] = useState({ count: list.length, minimum: 0 })

  useLayoutEffect(() => {
    const element = row.current
    if (!element) return
    const items = Array.from(element.children) as HTMLElement[]
    const more = items[list.length]!
    const measure = () => {
      const gap = Number.parseFloat(getComputedStyle(element).columnGap) || 0
      const widths = items.slice(0, list.length).map((item) => item.getBoundingClientRect().width)
      const moreWidth = more.getBoundingClientRect().width
      let count = list.length
      let width = widths.reduce((sum, value) => sum + value, 0) + gap * (count - 1)
      const available = element.getBoundingClientRect().width
      if (width > available) {
        width += moreWidth + gap
        while (count > pinned && width > available) {
          width -= widths[--count]! + gap
        }
      }
      const minimum = widths.slice(0, pinned).reduce((sum, value) => sum + value, 0)
        + gap * (pinned - 1) + (list.length > pinned ? moreWidth + gap : 0)
      const focused = items.findIndex((item) => item.contains(document.activeElement))
      if (focused >= count && focused < list.length) focusAfterResize.current = trigger.current
      if (count === list.length) {
        if (focused === list.length) focusAfterResize.current = items[0]!.querySelector('button')
        setOpen(false)
      }
      setLayout((previous) => previous.count === count && previous.minimum === minimum ? previous : { count, minimum })
    }
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(element)
    for (const item of items) observer.observe(item)
    return () => observer.disconnect()
  }, [list, pinned])

  useLayoutEffect(() => {
    focusAfterResize.current?.focus()
    focusAfterResize.current = null
  }, [layout])

  const overflow = list.slice(layout.count)
  return (
    <nav aria-label="Main navigation" className="min-w-0 flex-1" style={{ minWidth: layout.minimum }}>
      <ul ref={row} className="relative flex items-center gap-1">
        {list.map(({ name, label, Icon, tabs }, index) => {
          const hidden = index >= layout.count
          const selected = current === name || tabs?.includes(current)
          return (
            <li key={name} aria-hidden={hidden || undefined} inert={hidden} className={cn('w-max shrink-0', hidden && 'invisible absolute')}>
              <ListRow
                selected={selected}
                aria-current={selected ? 'page' : undefined}
                leading={<Icon />}
                onClick={() => navigate(name)}
              >
                {label}
              </ListRow>
            </li>
          )
        })}
        <li aria-hidden={overflow.length === 0 || undefined} inert={overflow.length === 0} className={cn('shrink-0', overflow.length === 0 && 'invisible absolute')}>
          <Menu open={open} onOpenChange={setOpen}>
            <MenuTrigger asChild>
              <Button ref={trigger} variant="ghost" size="icon" label="More navigation">
                <Ellipsis />
              </Button>
            </MenuTrigger>
            <MenuContent
              align="end"
              onCloseAutoFocus={(event) => {
                if (navigated.current) {
                  event.preventDefault()
                  navigated.current = false
                  focusView()
                } else if (overflow.length === 0) {
                  event.preventDefault()
                  const element = row.current
                  ;(element?.querySelector<HTMLElement>('button[aria-current="page"]') ?? element?.querySelector('button'))?.focus()
                }
              }}
            >
              {overflow.map(({ name, label, Icon, tabs }) => (
                <MenuItem
                  key={name}
                  aria-current={current === name || tabs?.includes(current) ? 'page' : undefined}
                  onSelect={() => {
                    navigated.current = true
                    navigate(name)
                  }}
                >
                  <Icon />
                  {label}
                </MenuItem>
              ))}
            </MenuContent>
          </Menu>
        </li>
      </ul>
    </nav>
  )
}

function PhoneToolbar() {
  const cap = useCapability()
  const role = useSelfRole()
  const blocked = useStore((s) => launchBlocked(s.linkStatus))
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
    <>
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
          <Button variant="ghost" size="icon" label="New run" hint={blocked} aria-disabled={blocked ? true : undefined} onClick={() => !blocked && openDialog('launch')}>
            <Plus />
          </Button>
        ) : (
          <Button size="sm" hint={blocked} aria-disabled={blocked ? true : undefined} onClick={() => !blocked && openDialog('launch')}>
            <Plus />
            New run
          </Button>
        ))}
      </div>
      <ConnectionLine className="px-3 pb-1.5" />
    </>
  )
}
