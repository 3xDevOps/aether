import { type CSSProperties, type ReactNode, useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import { cn } from '@/lib/utils'

const slideMs = 220

/** True while `open`, and for the slide-out after it turns false, so a panel can leave without being unmounted at once. */
export function useLingering(open: boolean): boolean {
  const [lingering, setLingering] = useState(open)
  useEffect(() => {
    if (open) {
      setLingering(true)
      return
    }
    const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches
    const timer = setTimeout(() => setLingering(false), reduced ? 0 : slideMs)
    return () => clearTimeout(timer)
  }, [open])
  return open || lingering
}

/**
 * A surface that slides out of the sidebar's right edge over the view, with no
 * scrim. The edge clips it: it starts behind the sidebar and the clip leaves
 * its shadow free on every other side. With the sidebar hidden it slides in
 * from the window edge instead. `top` places a short surface; without it the
 * surface takes the sidebar's full height.
 */
export function SlideOut({ open, width, top, className, children }: {
  open: boolean
  /** A CSS length; the surface never grows past the room right of the sidebar. */
  width: string
  top?: number
  className?: string
  children: ReactNode
}) {
  const sidebar = document.getElementById('sidebar')
  const style: CSSProperties = { width: `min(${width}, ${sidebar ? 'calc(100vw - 100%)' : '100vw'})`, ...(top === undefined ? {} : { top }) }
  return createPortal(
    <div
      style={style}
      className={cn(
        'pointer-events-none z-40 [clip-path:inset(-2rem_-2rem_-2rem_0)]',
        sidebar ? 'absolute left-full' : 'fixed left-0',
        top === undefined && 'inset-y-0',
      )}
    >
      <div
        data-state={open ? 'open' : 'closed'}
        className={cn('pointer-events-auto h-full', open ? 'animate-sheet-left' : 'animate-sheet-left-out', 'motion-reduce:animate-none', className)}
      >
        {children}
      </div>
    </div>,
    sidebar ?? document.body,
  )
}
