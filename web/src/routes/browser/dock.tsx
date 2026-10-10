import type * as React from 'react'
import { useDrag } from '@/lib/hooks'
import { splitterTarget } from '@/lib/keys'
import { cn, focusRing } from '@/lib/utils'
import { useStore } from '@/store'

/** The narrowest either side of the divider may get. */
export const minBesideWidth = 320

/** The Browser beside a run's other views, with the divider that sizes it. */
export function BrowserDock({ available, children }: { available: number; children: React.ReactNode }) {
  const stored = useStore((s) => s.browserBesideWidth)
  const setWidth = useStore((s) => s.setBrowserBesideWidth)
  const beginDrag = useDrag()
  const max = available - minBesideWidth
  const clamp = (value: number) => Math.round(Math.min(max, Math.max(minBesideWidth, value)))
  const width = clamp(stored ?? available / 2)

  const startResize = (event: React.PointerEvent) => {
    event.preventDefault()
    const startX = event.clientX
    const drag = beginDrag()
    const move = (ev: PointerEvent) => setWidth(clamp(width + startX - ev.clientX))
    window.addEventListener('pointermove', move, { signal: drag.signal })
    for (const end of ['pointerup', 'pointercancel']) {
      window.addEventListener(end, () => drag.abort(), { signal: drag.signal })
    }
  }

  const resizeKey = (event: React.KeyboardEvent) => {
    const next = splitterTarget(event.key, { value: width, min: minBesideWidth, max, grow: 'ArrowLeft', shrink: 'ArrowRight' })
    if (next === null) return
    event.preventDefault()
    setWidth(clamp(next))
  }

  // `data-browser` puts the divider inside the Browser for its key bindings
  // and its lease: resizing the pane is not leaving it.
  return (
    <section id="run-browser" data-browser aria-label="Browser" style={{ width }} className="relative h-full shrink-0 border-l border-seam">
      <div
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize the Browser"
        aria-controls="run-browser"
        aria-valuenow={width}
        aria-valuemin={minBesideWidth}
        aria-valuemax={max}
        tabIndex={0}
        onPointerDown={startResize}
        onKeyDown={resizeKey}
        className={cn(
          focusRing,
          // Without `touch-none` the browser claims a touch drag as a pan
          // and cancels the pointer stream this listens to.
          'absolute inset-y-0 -left-1 z-10 w-2 cursor-col-resize touch-none hover:bg-hover-chrome coarse:-left-3 coarse:w-6',
        )}
      />
      {children}
    </section>
  )
}
