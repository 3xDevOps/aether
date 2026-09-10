import { ChevronDown, ChevronUp, Plus, X } from 'lucide-react'
import { useCallback, useEffect, useId, useRef, useState } from 'react'
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
  actions?: React.ReactNode
  children: React.ReactNode
}

const minDockHeight = 120

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
  const max = maxDockHeight(viewport)
  const index = Math.max(
    0,
    tabs.findIndex((tab) => tab.id === activeTab),
  )
  const [focused, setFocused] = useState(index)
  useEffect(() => setFocused(index), [index, tabs.length])
  const stop = Math.min(focused, tabs.length - 1)
  // Enter collapses, which unmounts this handle, so focus moves to the toggle
  // before the pane goes: that button is in the header either way.
  const collapse = useRef<HTMLButtonElement>(null)

  const startResize = useCallback(
    (event: React.PointerEvent<HTMLDivElement>) => {
      event.preventDefault()
      const startY = event.clientY
      const startHeight = height
      const drag = beginDrag()
      const move = (ev: PointerEvent) => {
        onHeightChange(clampDockHeight(startHeight + startY - ev.clientY, viewport))
      }
      window.addEventListener('pointermove', move, { signal: drag.signal })
      for (const end of ['pointerup', 'pointercancel']) {
        window.addEventListener(end, () => drag.abort(), { signal: drag.signal })
      }
    },
    [beginDrag, height, onHeightChange, viewport],
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
        value: height,
        min: minDockHeight,
        max,
        grow: 'ArrowUp',
        shrink: 'ArrowDown',
      })
      if (next === null) return
      event.preventDefault()
      onHeightChange(clampDockHeight(next, viewport))
    },
    [height, max, onHeightChange, onToggleCollapse, viewport],
  )

  return (
    <section
      id={dockID}
      className="relative flex shrink-0 flex-col border-t border-border/90 bg-card/35"
      style={collapsed ? undefined : { height: clampDockHeight(height, viewport) }}
      aria-label="Terminal dock"
    >
      {!collapsed && (
        <div
          role="separator"
          aria-orientation="horizontal"
          aria-label="Resize terminal dock"
          aria-controls={dockID}
          aria-valuenow={Math.round(clampDockHeight(height, viewport))}
          aria-valuemin={minDockHeight}
          aria-valuemax={Math.round(max)}
          tabIndex={0}
          onPointerDown={startResize}
          onKeyDown={resizeKey}
          className={cn(
            focusRing,
            'absolute inset-x-0 -top-1 z-10 h-2 cursor-row-resize rounded-sm bg-transparent transition-colors hover:bg-primary/20 focus-visible:bg-primary/20',
          )}
        />
      )}
      <div className="flex h-10 min-h-10 items-center gap-1 border-b border-border/75 bg-background/65 px-2">
        <div className="flex min-w-0 flex-1 items-center gap-1">
          <div
            className="flex min-w-0 max-w-full items-center gap-1 overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
            role={tabs.length > 0 ? 'tablist' : undefined}
            aria-label={tabs.length > 0 ? 'Terminal tabs' : undefined}
          >
            {tabs.map((tab, i) => (
              <div
                key={tab.id}
                className={cn(
                  'flex min-w-0 shrink-0 items-center rounded-md border border-transparent',
                  activeTab === tab.id &&
                    'border-primary/20 bg-[var(--accent-soft)] text-[var(--accent-soft-foreground)]',
                )}
              >
                <button
                  type="button"
                  role="tab"
                  id={tabID(tab.id)}
                  aria-selected={activeTab === tab.id}
                  aria-controls={
                    !collapsed && activeTab === tab.id ? panelID : undefined
                  }
                  tabIndex={i === stop ? 0 : -1}
                  className={cn(
                    focusRing,
                    'min-h-8 min-w-0 max-w-40 truncate rounded-sm px-2.5 py-1.5 text-[13px] font-medium',
                  )}
                  onFocus={() => setFocused(i)}
                  onClick={() => onSelectTab(tab.id)}
                  onKeyDown={(event) =>
                    onTabListKeyDown(event, tabs.length, stop, setFocused)
                  }
                >
                  {tab.label}
                </button>
                {onCloseTab && !tab.permanent && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    className="mr-0.5 size-7 rounded-sm"
                    aria-label={`Close ${tab.label}`}
                    onClick={(event) => {
                      event.stopPropagation()
                      onCloseTab(tab.id)
                    }}
                  >
                    <X />
                  </Button>
                )}
              </div>
            ))}
          </div>
          {onAddTab && (
            <Button
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
            <span role="status" className="px-1 text-xs text-muted-foreground">
              At most {maxTabs} tabs
            </span>
          )}
        </div>
        {actions}
        <Button
          ref={collapse}
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
          className={cn(focusRing, 'min-h-0 flex-1')}
        >
          {children}
        </div>
      )}
    </section>
  )
}
