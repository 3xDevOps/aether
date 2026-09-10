import { useEffect, useRef, useState } from 'react'
import { onTabListKeyDown } from '@/lib/keys'
import { cn, focusRing } from '@/lib/utils'
import { useStore } from '@/store'
import type { Route } from '@/store/ui'

/** The run-detail tabs, in strip order. Exported so a caller that has to
 * reason about "any tab of this run" reads the same list the strip draws. */
export const runTabs = [
  { route: 'run', label: 'Overview' },
  { route: 'terminal', label: 'Terminal' },
  { route: 'diff', label: 'Diff' },
  { route: 'events', label: 'Events' },
]

/**
 * True while any run-detail tab for this run is open. Every entry point
 * lands on the terminal, so a selected row cannot key on the Overview route
 * alone and still stay lit while the reader moves between tabs.
 */
export function isRunRoute(route: Route, runID: string): boolean {
  return route.params.runId === runID && runTabs.some((t) => t.route === route.name)
}

const panelID = 'run-tab-panel'
const tabID = (route: string) => `run-tab-${route}`

/**
 * The props the open route spreads on the body under the strip. Only one run
 * route is mounted at a time, so the ids are fixed rather than generated.
 */
export function runTabPanel(active: string, className: string, scrolls = false) {
  return {
    role: 'tabpanel',
    id: panelID,
    'aria-labelledby': tabID(active),
    // A tab stop only where the panel is the scroller and holds nothing
    // focusable of its own; the other two would be a stop that scrolls
    // nothing.
    tabIndex: scrolls ? 0 : undefined,
    className: cn(scrolls && focusRing, className),
  } as const
}

/** The run and tab a keyboard activation asked for, so the strip the next
 * route draws can take the focus the unmounted one was holding. */
let pendingFocus: string | null = null

/** The run-detail tab strip. Each tab is a route the registry already knows. */
export function RunTabs({ runID, active }: { runID: string; active: string }) {
  const navigate = useStore((s) => s.navigate)
  const index = Math.max(
    0,
    runTabs.findIndex((t) => t.route === active),
  )
  const [focused, setFocused] = useState(index)
  const selected = useRef<HTMLButtonElement>(null)

  useEffect(() => setFocused(index), [index, runID])

  useEffect(() => {
    // Consumed whether it matched or not, so an activation that ends up
    // drawing no strip cannot leave this armed for a later mouse click.
    const want = pendingFocus
    pendingFocus = null
    if (want === `${runID}:${active}`) selected.current?.focus()
  }, [runID, active])

  return (
    <div
      role="tablist"
      aria-label="Run tabs"
      className="flex h-9 min-h-9 items-end gap-0 overflow-x-auto border-b border-border bg-sidebar px-3 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
    >
      {runTabs.map(({ route, label }, i) => (
        <button
          key={route}
          ref={route === active ? selected : undefined}
          type="button"
          role="tab"
          id={tabID(route)}
          aria-selected={route === active}
          aria-controls={route === active ? panelID : undefined}
          tabIndex={i === focused ? 0 : -1}
          onFocus={() => setFocused(i)}
          onClick={(event) => {
            // Armed from the activation itself, never from a key press that
            // may still be cancelled, and never for a pointer click: a click
            // the keyboard produced carries no click count, and the browser
            // has already focused a button the pointer hit. Reopening the tab
            // already open navigates nowhere, so nothing would consume it.
            if (event.detail === 0 && route !== active) {
              pendingFocus = `${runID}:${route}`
            }
            navigate(route, { runId: runID })
          }}
          onKeyDown={(event) =>
            onTabListKeyDown(event, runTabs.length, focused, setFocused)
          }
          className={cn(
            focusRing,
            'min-h-9 shrink-0 rounded-none border-0 border-b-2 border-transparent px-3 py-0 text-[13px] font-medium transition-[background-color,border-color,color,box-shadow] duration-100 motion-reduce:transition-none',
            route === active
              ? 'border-b-primary bg-transparent text-foreground'
              : 'text-muted-foreground hover:bg-toolbar-hover hover:text-foreground',
          )}
        >
          {label}
        </button>
      ))}
    </div>
  )
}
