import { ChevronDown, ChevronUp, Plus, X } from 'lucide-react'
import { useCallback } from 'react'
import type * as React from 'react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

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
  onCloseTab?: (id: string) => void
  height: number
  onHeightChange: (height: number) => void
  collapsed: boolean
  onToggleCollapse: () => void
  actions?: React.ReactNode
  children: React.ReactNode
}

export function clampDockHeight(px: number): number {
  const min = 120
  const max = Math.max(min, (typeof window === 'undefined' ? 320 : window.innerHeight) - 200)
  return Math.min(max, Math.max(min, px))
}

export function Dock({
  tabs,
  activeTab,
  onSelectTab,
  onAddTab,
  onCloseTab,
  height,
  onHeightChange,
  collapsed,
  onToggleCollapse,
  actions,
  children,
}: DockProps) {
  const startResize = useCallback(
    (event: React.PointerEvent<HTMLDivElement>) => {
      event.preventDefault()
      const startY = event.clientY
      const startHeight = height
      const move = (ev: PointerEvent) => {
        onHeightChange(clampDockHeight(startHeight + startY - ev.clientY))
      }
      const stop = () => {
        window.removeEventListener('pointermove', move)
        window.removeEventListener('pointerup', stop)
      }
      window.addEventListener('pointermove', move)
      window.addEventListener('pointerup', stop)
    },
    [height, onHeightChange],
  )

  return (
    <section
      className="relative flex shrink-0 flex-col border-t bg-background"
      style={collapsed ? undefined : { height: clampDockHeight(height) }}
      aria-label="Terminal dock"
    >
      {!collapsed && (
        <div
          role="separator"
          aria-orientation="horizontal"
          aria-label="Resize terminal dock"
          onPointerDown={startResize}
          className="absolute inset-x-0 -top-1 z-10 h-2 cursor-row-resize hover:bg-accent"
        />
      )}
      <div className="flex h-9 min-h-9 items-center gap-1 border-b px-2">
        <div className="flex min-w-0 flex-1 items-center gap-1" role="tablist">
          {tabs.map((tab) => (
            <div
              key={tab.id}
              className={cn(
                'flex min-w-0 items-center rounded-md',
                activeTab === tab.id && 'bg-accent',
              )}
            >
              <button
                type="button"
                role="tab"
                aria-selected={activeTab === tab.id}
                tabIndex={activeTab === tab.id ? 0 : -1}
                className="truncate px-2 py-1 text-xs font-medium outline-none focus-visible:ring-[2px] focus-visible:ring-ring/50"
                onClick={() => onSelectTab(tab.id)}
              >
                {tab.label}
              </button>
              {onCloseTab && !tab.permanent && (
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="mr-0.5 size-5"
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
          {onAddTab && (
            <Button
              type="button"
              variant="ghost"
              size="icon"
              aria-label="Add terminal tab"
              onClick={onAddTab}
            >
              <Plus />
            </Button>
          )}
        </div>
        {actions}
        <Button
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
      {!collapsed && <div className="min-h-0 flex-1">{children}</div>}
    </section>
  )
}
