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
