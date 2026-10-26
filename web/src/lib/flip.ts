import { type RefObject, useCallback, useLayoutEffect, useRef } from 'react'
import { prefersReducedMotion } from '@/lib/hooks'
import { type Motion, play } from '@/lib/motion'

// Opt-in layout glides for short lists the viewer reorders: never on a
// virtualized list, whose rows are recycled, nor on store ticks.

export type Box = Pick<DOMRectReadOnly, 'left' | 'top'>

const glides = new WeakMap<Element, Animation>()

/**
 * Plays `el` from `from` to where it is now, translating only so text never
 * stretches. A glide still running is replaced, and a capture taken during it
 * already holds the element's visual position, so the next one continues.
 */
export function glideFrom(el: HTMLElement, from: Box, motion: Motion): Animation | null {
  glides.get(el)?.cancel()
  glides.delete(el)
  if (prefersReducedMotion()) return null
  const now = el.getBoundingClientRect()
  const dx = from.left - now.left
  const dy = from.top - now.top
  if (!dx && !dy) return null
  const animation = play(el, [{ translate: `${dx}px ${dy}px` }, { translate: '0 0' }], motion, { composite: 'add' })
  glides.set(el, animation)
  return animation
}

export function capture(container: HTMLElement | null): Map<string, Box> {
  const boxes = new Map<string, Box>()
  container?.querySelectorAll<HTMLElement>('[data-glide-key]').forEach((el) => {
    boxes.set(el.dataset.glideKey!, el.getBoundingClientRect())
  })
  return boxes
}

/**
 * Mark the moving elements with `data-glide-key`, call `capture()` right
 * before the state change, and every element present before and after glides
 * once the owner commits. Elements that only enter or leave do not move.
 */
export function useLayoutGlide(container: RefObject<HTMLElement | null>, motion: Motion): () => void {
  const pending = useRef<Map<string, Box> | null>(null)
  const latest = useRef(motion)
  latest.current = motion

  useLayoutEffect(() => {
    const boxes = pending.current
    if (!boxes) return
    pending.current = null
    container.current?.querySelectorAll<HTMLElement>('[data-glide-key]').forEach((el) => {
      const from = boxes.get(el.dataset.glideKey!)
      if (from) glideFrom(el, from, latest.current)
    })
  })

  return useCallback(() => {
    pending.current = capture(container.current)
  }, [container])
}

const heights = new WeakMap<HTMLElement, { animation: Animation; overflow: string }>()

function stopHeightGlide(el: HTMLElement) {
  const glide = heights.get(el)
  if (!glide) return
  heights.delete(el)
  glide.animation.cancel()
  el.style.overflow = glide.overflow
}

/**
 * Plays `el` from `from` px tall to `to` (default: its height now), clipped
 * while it moves, then leaves its height to layout again. A glide still
 * running is replaced from the height it shows.
 */
export function glideHeight(el: HTMLElement, from: number, motion: Motion, to?: number): Animation | null {
  stopHeightGlide(el)
  const end = to ?? el.getBoundingClientRect().height
  if (prefersReducedMotion() || Math.abs(from - end) < 0.5) return null
  const overflow = el.style.overflow
  el.style.overflow = 'clip'
  const animation = play(el, [{ height: `${from}px` }, { height: `${end}px` }], motion, { fill: 'forwards' })
  heights.set(el, { animation, overflow })
  const done = () => {
    if (heights.get(el)?.animation === animation) stopHeightGlide(el)
  }
  animation.finished.then(done, done)
  return animation
}

/** Glides `ref` from the height it last painted to its new one whenever `key` changes. */
export function useHeightGlide(ref: RefObject<HTMLElement | null>, key: unknown, motion: Motion) {
  const last = useRef<{ key: unknown; height: number } | null>(null)
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const prior = last.current
    if (!prior || prior.key === key) {
      if (!heights.has(el)) last.current = { key, height: el.getBoundingClientRect().height }
      return
    }
    const from = heights.has(el) ? el.getBoundingClientRect().height : prior.height
    stopHeightGlide(el)
    const to = el.getBoundingClientRect().height
    last.current = { key, height: to }
    glideHeight(el, from, motion, to)
  })
}
