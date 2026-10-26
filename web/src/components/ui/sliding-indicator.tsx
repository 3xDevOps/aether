import { useLayoutEffect, useRef } from 'react'
import { useReducedMotion } from '@/lib/hooks'
import { type Motion, play } from '@/lib/motion'
import { cn } from '@/lib/utils'

type Box = { x: number; y: number; w: number; h: number }

const glide: Motion = { duration: '--duration-glide', easing: '--ease-glide' }

// Offsets inside the container, not viewport coordinates, so the indicator
// scrolls with its items in a scrolling strip. Client rects include ancestor
// transforms, such as a dialog scaling in, so they are divided back to layout
// pixels; a transform never fires the ResizeObserver to correct them later.
function boxOf(container: HTMLElement, el: Element): Box {
  const c = container.getBoundingClientRect()
  const r = el.getBoundingClientRect()
  const sx = container.offsetWidth ? c.width / container.offsetWidth : 1
  const sy = container.offsetHeight ? c.height / container.offsetHeight : 1
  return {
    x: (r.left - c.left) / sx - container.clientLeft + container.scrollLeft,
    y: (r.top - c.top) / sy - container.clientTop + container.scrollTop,
    w: r.width / sx,
    h: r.height / sy,
  }
}

const frame = (b: Box) => ({ left: `${b.x}px`, top: `${b.y}px`, width: `${b.w}px`, height: `${b.h}px` })
const same = (a: Box, b: Box) => a.x === b.x && a.y === b.y && a.w === b.w && a.h === b.h

/**
 * One decorative element behind the active item of a set, gliding to the next
 * active item rather than reappearing on it. Items mark themselves with
 * `data-indicator-key`; the parent element is the container and must be
 * positioned, with the items positioned too so they paint above it.
 */
export function SlidingIndicator({ activeKey, className }: { activeKey: string | undefined; className?: string }) {
  const ref = useRef<HTMLSpanElement>(null)
  const box = useRef<Box | null>(null)
  const moving = useRef<Animation | null>(null)
  const shownKey = useRef(activeKey)
  const observer = useRef<ResizeObserver | null>(null)
  const watched = useRef(new Set<Element>())
  const reduced = useReducedMotion()
  const reducedRef = useRef(reduced)
  reducedRef.current = reduced

  const place = useRef((animate: boolean) => {
    const el = ref.current
    const container = el?.parentElement
    if (!el || !container) return
    const target = [...container.querySelectorAll<HTMLElement>('[data-indicator-key]')].find(
      (item) => item.dataset.indicatorKey === shownKey.current,
    )
    if (!target) {
      moving.current?.cancel()
      moving.current = null
      box.current = null
      el.style.opacity = '0'
      return
    }
    const next = boxOf(container, target)
    // Mid-glide the visible box is the start, so a retarget continues from it.
    const from = moving.current ? boxOf(container, el) : box.current
    moving.current?.cancel()
    moving.current = null
    Object.assign(el.style, frame(next), { opacity: '1' })
    box.current = next
    if (!animate || !from || same(from, next) || reducedRef.current) return
    const animation = play(el, [frame(from), frame(next)], glide)
    moving.current = animation
    animation.finished.then(
      () => {
        if (moving.current !== animation) return
        moving.current = null
        place.current(false)
      },
      () => {},
    )
  })

  const watch = useRef(() => {
    if (!observer.current) return
    const items = new Set(ref.current?.parentElement?.querySelectorAll('[data-indicator-key]'))
    for (const item of watched.current) if (!items.has(item)) observer.current.unobserve(item)
    for (const item of items) if (!watched.current.has(item)) observer.current.observe(item)
    watched.current = items
  })

  // Every render, since a caller re-renders the set when items come or go and
  // the active one moves without a change of key.
  useLayoutEffect(() => {
    const changed = shownKey.current !== activeKey
    shownKey.current = activeKey
    if (reduced) {
      moving.current?.cancel()
      moving.current = null
    }
    if (!changed && moving.current) return
    place.current(changed && box.current !== null)
    watch.current()
  })

  // Labels, fonts and text size change item widths without a render.
  useLayoutEffect(() => {
    const container = ref.current?.parentElement
    if (!container) return
    observer.current = new ResizeObserver(() => {
      if (!moving.current) place.current(false)
    })
    observer.current.observe(container)
    watch.current()
    return () => {
      observer.current?.disconnect()
      watched.current.clear()
    }
  }, [])

  return (
    <span
      ref={ref}
      aria-hidden
      data-slot="sliding-indicator"
      className={cn('pointer-events-none absolute top-0 left-0 opacity-0', className)}
    />
  )
}
