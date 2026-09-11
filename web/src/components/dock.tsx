import { ChevronDown, ChevronUp, Plus, X } from 'lucide-react'
import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from 'react'
import type * as React from 'react'
import { Button } from '@/components/ui/button'
import { useDrag, useWindowHeight } from '@/lib/hooks'
import { onTabListKeyDown, splitterTarget } from '@/lib/keys'
import { cn, focusRing } from '@/lib/utils'

export interface DockTab {
  id: string
  label: string
  permanent?: boolean
}

export type DockContainment = 'viewport' | 'parent'

export interface DockProps {
  tabs: DockTab[]
  activeTab: string
  onSelectTab: (id: string) => void
  onAddTab?: () => void
  /** The dock's own tab ceiling; the limit text names this number. */
  maxTabs: number
  onCloseTab?: (id: string) => void
  height: number
  onHeightChange: (height: number) => void
  collapsed: boolean
  onToggleCollapse: () => void
  /**
   * Use the immediate parent as a fixed-size boundary, or keep the dock
   * independent of intrinsic parent sizing and use the viewport cap.
   */
  containment?: DockContainment
  actions?: React.ReactNode
  children: React.ReactNode
}

const minDockHeight = 120
// The panel needs its guidance, 36px terminal toolbar, and one terminal row.
const minDockBodyHeight = 96
const defaultDockHeaderHeight = 36

/** What is left of the window once the shell's own chrome has its share. */
function maxDockHeight(viewport: number): number {
  return Math.max(minDockHeight, viewport - 200)
}

export function clampDockHeight(px: number, viewport = window.innerHeight): number {
  return Math.min(maxDockHeight(viewport), Math.max(minDockHeight, px))
}

export function Dock({
  tabs,
  activeTab,
  onSelectTab,
  onAddTab,
  maxTabs,
  onCloseTab,
  height,
  onHeightChange,
  collapsed,
  onToggleCollapse,
  containment = 'viewport',
  actions,
  children,
}: DockProps) {
  const atLimit = tabs.length >= maxTabs
  const id = useId()
  const tabID = (tab: string) => `${id}-tab-${tab}`
  const panelID = `${id}-panel`
  const dockID = `${id}-dock`
  const viewport = useWindowHeight()
  const beginDrag = useDrag()
  const dockRef = useRef<HTMLElement>(null)
  const headerRef = useRef<HTMLDivElement>(null)
  const [headerHeight, setHeaderHeight] = useState(defaultDockHeaderHeight)
  const [parentHeight, setParentHeight] = useState<number | null>(null)
  // Parent-contained callers give the primary sibling a CSS minimum. Reserve
  // that declared constraint, rather than its changing flex height.
  const [primaryMinHeight, setPrimaryMinHeight] = useState(0)
  useLayoutEffect(() => {
    const dockElement = dockRef.current
    if (!dockElement) return
    const parent = containment === 'parent' ? dockElement.parentElement : null
    const primary = parent?.firstElementChild

    const measure = () => {
      const measuredHeader = headerRef.current?.getBoundingClientRect().height ?? 0
      const measuredPrimary =
        primary ? Number.parseFloat(getComputedStyle(primary).minHeight) : 0
      if (measuredHeader > 0) setHeaderHeight(Math.ceil(measuredHeader))
      setPrimaryMinHeight(
        Number.isFinite(measuredPrimary) ? Math.max(0, Math.ceil(measuredPrimary)) : 0,
      )
      setParentHeight(
        parent ? Math.max(0, Math.floor(parent.getBoundingClientRect().height)) : null,
      )
    }
    measure()
    const observer = new ResizeObserver(measure)
    if (headerRef.current) observer.observe(headerRef.current)
    if (primary) observer.observe(primary)
    if (parent) observer.observe(parent)
    return () => observer.disconnect()
  }, [containment])
  const viewportMax = maxDockHeight(viewport)
  const requiredMinimum = Math.max(minDockHeight, Math.ceil(headerHeight) + minDockBodyHeight)
  const max =
    parentHeight === null
      ? viewportMax
      : parentHeight === 0
        ? 0
        : Math.max(
            requiredMinimum,
            Math.min(viewportMax, Math.max(0, parentHeight - primaryMinHeight)),
          )
  const min = Math.min(requiredMinimum, max)
  const currentHeight = Math.min(max, Math.max(min, height))
  const index = Math.max(
    0,
    tabs.findIndex((tab) => tab.id === activeTab),
  )
  const [focused, setFocused] = useState(index)
  // A consumer may remove the requested tab after an async store update. Keep
  // its position until it is actually gone, then focus the surviving DOM tab.
  const pendingClose = useRef<{
    id: string
    index: number
    // Null means an inactive tab was closed, so its removal must not steal focus.
    focus: Element | null
  } | null>(null)
  useEffect(() => {
    const cancelPendingClose = (event: FocusEvent) => {
      const close = pendingClose.current
      if (!close?.focus || event.target === close.focus) return
      const bodyFocusFromClosedTab =
        event.target === document.body && !close.focus.isConnected
      if (!bodyFocusFromClosedTab) pendingClose.current = null
    }
    document.addEventListener('focusin', cancelPendingClose)
    return () => document.removeEventListener('focusin', cancelPendingClose)
  }, [])
  const focusRepairLength = useRef<number | null>(null)
  const addTab = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    if (focusRepairLength.current === tabs.length) {
      focusRepairLength.current = null
      return
    }
    setFocused(index)
  }, [index, tabs.length])
  useLayoutEffect(() => {
    const close = pendingClose.current
    if (!close || tabs.some((tab) => tab.id === close.id)) return
    const focusWasLostWithClosedTab =
      document.activeElement === close.focus ||
      (document.activeElement === document.body &&
        close.focus !== null &&
        !close.focus.isConnected)
    pendingClose.current = null
    if (!focusWasLostWithClosedTab) return
    focusRepairLength.current = tabs.length
    const next = tabs.length === 0 ? -1 : Math.min(close.index, tabs.length - 1)
    if (next < 0) {
      setFocused(0)
      addTab.current?.focus()
      return
    }
    setFocused(next)
    headerRef.current?.querySelectorAll<HTMLElement>('[role="tab"]')[next]?.focus()
  }, [tabs])
  const requestClose = useCallback(
    (tab: DockTab, tabIndex: number, owner: Element) => {
      if (!onCloseTab || tab.permanent) return
      pendingClose.current = {
        id: tab.id,
        index: tabIndex,
        focus: document.activeElement === owner ? owner : null,
      }
      onCloseTab(tab.id)
    },
    [onCloseTab],
  )
  const stop = Math.min(focused, tabs.length - 1)
  // Enter collapses, which unmounts this handle, so focus moves to the toggle
  // before the pane goes: that button is in the header either way.
  const collapse = useRef<HTMLButtonElement>(null)

  const startResize = useCallback(
    (event: React.PointerEvent<HTMLDivElement>) => {
      event.preventDefault()
      const startY = event.clientY
      const startHeight = currentHeight
      const drag = beginDrag()
      const move = (ev: PointerEvent) => {
        onHeightChange(
          Math.min(max, Math.max(min, startHeight + startY - ev.clientY)),
        )
      }
      window.addEventListener('pointermove', move, { signal: drag.signal })
      for (const end of ['pointerup', 'pointercancel']) {
        window.addEventListener(end, () => drag.abort(), { signal: drag.signal })
      }
    },
    [beginDrag, currentHeight, max, min, onHeightChange],
  )

  const resizeKey = useCallback(
    (event: React.KeyboardEvent<HTMLDivElement>) => {
      if (event.key === 'Enter') {
        event.preventDefault()
        collapse.current?.focus()
        onToggleCollapse()
        return
      }
      const next = splitterTarget(event.key, {
        value: currentHeight,
        min,
        max,
        grow: 'ArrowUp',
        shrink: 'ArrowDown',
      })
      if (next === null) return
      event.preventDefault()
      onHeightChange(Math.min(max, Math.max(min, next)))
    },
    [currentHeight, max, min, onHeightChange, onToggleCollapse],
  )

  return (
    <section
      ref={dockRef}
      id={dockID}
      className="relative flex min-h-0 shrink-0 flex-col border-t border-border bg-sidebar"
      style={collapsed ? undefined : { height: currentHeight }}
      aria-label="Terminal dock"
    >
      {!collapsed && (
        <div
          role="separator"
          aria-orientation="horizontal"
          aria-label="Resize terminal dock"
          aria-controls={dockID}
          aria-valuenow={Math.round(currentHeight)}
          aria-valuemin={Math.round(min)}
          aria-valuemax={Math.round(max)}
          tabIndex={0}
          onPointerDown={startResize}
          onKeyDown={resizeKey}
          className={cn(
            focusRing,
            // Without `touch-none` the browser claims a touch drag as a pan
            // and cancels the pointer stream this listens to. The coarse hit
            // area is 24px centred on the edge, the same as the sidebar's.
            'absolute inset-x-0 -top-px z-10 h-1 cursor-row-resize touch-none bg-transparent transition-colors hover:bg-primary/20 focus-visible:bg-primary/20 coarse:-top-3 coarse:h-6',
          )}
        />
      )}
      <div
        ref={headerRef}
        className="flex min-h-9 flex-wrap items-center gap-x-1 border-b border-border bg-sidebar px-2"
      >
        <div className="flex min-w-0 flex-1 items-center gap-1">
          <div
            className="flex min-w-0 max-w-full items-center gap-0 overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
            role={tabs.length > 0 ? 'tablist' : undefined}
            aria-label={tabs.length > 0 ? 'Terminal tabs' : undefined}
          >
            {tabs.map((tab, i) => (
              <div
                key={tab.id}
                className={cn(
                  'flex min-w-0 shrink-0 items-center border-b-2 border-transparent text-muted-foreground',
                  activeTab === tab.id && 'border-b-primary text-foreground',
                )}
              >
                {/* Prevent the native tab button from taking focus or activating
                    before the close has completed. */}
                <button
                  type="button"
                  role="tab"
                  id={tabID(tab.id)}
                  aria-selected={activeTab === tab.id}
                  aria-controls={
                    !collapsed && activeTab === tab.id ? panelID : undefined
                  }
                  aria-keyshortcuts={
                    onCloseTab && !tab.permanent ? 'Delete Backspace' : undefined
                  }
                  tabIndex={i === stop ? 0 : -1}
                  className={cn(
                    focusRing,
                    'inline-flex min-h-9 min-w-0 max-w-40 items-center gap-0 truncate rounded-none border-0 px-2.5 py-0 text-[13px] font-medium',
                  )}
                  onFocus={() => setFocused(i)}
                  onPointerDown={(event) => {
                    if (
                      event.button !== 0 ||
                      !(event.target instanceof Element) ||
                      !event.target.closest('[data-tab-close]')
                    ) {
                      return
                    }
                    event.preventDefault()
                    event.stopPropagation()
                  }}
                  onClick={(event) => {
                    if (
                      event.button === 0 &&
                      event.target instanceof Element &&
                      event.target.closest('[data-tab-close]')
                    ) {
                      event.preventDefault()
                      event.stopPropagation()
                      requestClose(tab, i, event.currentTarget)
                      return
                    }
                    onSelectTab(tab.id)
                  }}
                  onKeyDown={(event) => {
                    if (
                      onCloseTab &&
                      !tab.permanent &&
                      !event.altKey &&
                      !event.ctrlKey &&
                      !event.metaKey &&
                      !event.shiftKey &&
                      (event.key === 'Delete' || event.key === 'Backspace')
                    ) {
                      event.preventDefault()
                      requestClose(tab, i, event.currentTarget)
                      return
                    }
                    onTabListKeyDown(event, tabs.length, stop, setFocused)
                  }}
                >
                  <span className="min-w-0 flex-1 truncate">{tab.label}</span>
                  {onCloseTab && !tab.permanent && (
                    <span
                      data-tab-close
                      aria-hidden="true"
                      className="mr-0.5 inline-flex size-[22px] shrink-0 items-center justify-center rounded-none text-muted-foreground transition-[background-color,color] duration-100 hover:bg-toolbar-hover hover:text-foreground active:bg-toolbar-hover motion-reduce:transition-none"
                    >
                      <X className="pointer-events-none size-4" />
                    </span>
                  )}
                </button>
              </div>
            ))}
          </div>
          {onAddTab && (
            <Button
              ref={addTab}
              type="button"
              variant="ghost"
              size="icon"
              aria-label="Add terminal tab"
              disabled={atLimit}
              onClick={onAddTab}
            >
              <Plus />
            </Button>
          )}
          {atLimit && (
            // A disabled control shows no tooltip, so the ceiling is written
            // out instead of hidden in a title attribute.
            <span role="status" className="px-1 text-[12px] text-muted-foreground">
              At most {maxTabs} tabs
            </span>
          )}
        </div>
        {!collapsed && actions && (
          <div className="flex min-w-0 max-w-[52%] shrink-0 items-center justify-end gap-1 overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden max-[640px]:order-3 max-[640px]:max-w-full max-[640px]:basis-full max-[640px]:justify-end max-[640px]:border-t max-[640px]:border-border max-[640px]:py-1">
            {actions}
          </div>
        )}
        <Button
          ref={collapse}
          className="max-[640px]:order-2"
          type="button"
          variant="ghost"
          size="icon"
          aria-label={collapsed ? 'Expand terminal dock' : 'Collapse terminal dock'}
          aria-expanded={!collapsed}
          onClick={onToggleCollapse}
        >
          {collapsed ? <ChevronUp /> : <ChevronDown />}
        </Button>
      </div>
      {!collapsed && (
        <div
          role={tabs.length > 0 ? 'tabpanel' : undefined}
          id={panelID}
          aria-labelledby={tabs[index] ? tabID(tabs[index].id) : undefined}
          tabIndex={tabs.length > 0 ? 0 : undefined}
          className={cn(focusRing, 'min-h-0 min-w-0 flex-1 overflow-hidden')}
        >
          {children}
        </div>
      )}
    </section>
  )
}
