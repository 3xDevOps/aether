import { type RefObject, useCallback, useLayoutEffect, useRef, useState } from 'react'
import { prefersReducedMotion } from '@/lib/hooks'
import { type Motion, play, playEntrance, time } from '@/lib/motion'
import type { MapNode, RunMapLayout } from '@/routes/board/map-layout'

// A repack moves units and nodes by layout coordinates, never by measuring the
// DOM: the world is scaled, so screen deltas would be off by the zoom.

const glide: Motion = { duration: '--duration-glide', easing: '--ease-glide' }
const enter: Motion = { duration: '--duration-220', easing: '--ease-enter', delay: '--duration-150' }
const fade: Motion = { duration: '--duration-130', easing: '--ease-standard' }
const staggerCap = 8

type Place = { x: number; y: number; parent?: string }

function places(layout: RunMapLayout): Map<string, Place> {
  const map = new Map<string, Place>()
  for (const section of layout.sections) {
    map.set(`s:${section.key}`, section)
    for (const unit of section.units) {
      map.set(`u:${unit.key}`, { x: unit.x, y: unit.y, parent: `s:${section.key}` })
      for (const node of unit.nodes) map.set(`n:${node.key}`, { x: node.x, y: node.y, parent: `u:${unit.key}` })
    }
  }
  return map
}

/**
 * Glides what a repack moved, fades in new nodes and fades out removed ones
 * (returned as `ghosts` to draw at their old place), and runs the fold: child runs
 * fade last to first before `commit` repacks without them. The folded or
 * unfolded parent stays put on screen: `follow` pans the camera by its
 * move in world units, and the glides start from where things were seen.
 */
export function useMapMotion(
  world: RefObject<HTMLElement | null>,
  layout: RunMapLayout,
  commit: (unitKey: string, folded: boolean) => void,
  follow: (dx: number, dy: number) => void,
) {
  const previous = useRef(layout)
  const anchor = useRef<string | null>(null)
  const faded = useRef(new Set<string>())
  const folding = useRef(new Map<string, Animation[]>())
  const [ghosts, setGhosts] = useState<MapNode[]>([])

  useLayoutEffect(() => {
    const before = previous.current
    previous.current = layout
    const root = world.current
    const held = anchor.current
    anchor.current = null
    if (before.revision === layout.revision || !root) {
      faded.current.clear()
      return
    }
    const old = places(before)
    const next = places(layout)
    const from = held ? old.get(`n:${held}`) : undefined
    const to = held ? next.get(`n:${held}`) : undefined
    const shift = from && to ? { x: to.x - from.x, y: to.y - from.y } : { x: 0, y: 0 }
    if (shift.x || shift.y) follow(shift.x, shift.y)
    if (prefersReducedMotion()) {
      faded.current.clear()
      return
    }
    const offset = (key: string | undefined): { x: number; y: number } => {
      const from = key ? old.get(key) : undefined
      const to = key ? next.get(key) : undefined
      return from && to ? { x: from.x - to.x, y: from.y - to.y } : { x: 0, y: 0 }
    }
    let entering = 0
    root.querySelectorAll<HTMLElement>('[data-glide-key]').forEach((el) => {
      const key = el.dataset.glideKey!
      if (!old.has(key)) {
        if (key.startsWith('n:') && before.nodes.length) {
          playEntrance(el, { motion: enter, rise: 6, index: Math.min(entering++, staggerCap) })
        }
        return
      }
      const own = offset(key)
      const above = next.get(key)?.parent
      const parent = above ? offset(above) : { x: -shift.x, y: -shift.y }
      const dx = own.x - parent.x
      const dy = own.y - parent.y
      // Added to a glide still running, so the element continues from where it is seen.
      if (dx || dy) play(el, [{ translate: `${dx}px ${dy}px` }, { translate: '0 0' }], glide, { composite: 'add' })
    })
    if (before.nodes.length) {
      const hadLines = new Set(before.units.filter((unit) => unit.lines.length).map((unit) => `u:${unit.key}`))
      root.querySelectorAll<HTMLElement>('[data-map-unit]').forEach((el) => {
        const lines = el.querySelector(':scope > [data-map-connectors]')
        if (lines && !hadLines.has(el.dataset.glideKey!)) playEntrance(lines, { motion: fade })
      })
    }
    const kept = new Set(layout.nodes.map((node) => node.key))
    const removed = before.nodes.filter((node) => !kept.has(node.key) && !faded.current.has(node.key))
    faded.current.clear()
    if (removed.length) setGhosts((current) => [...current, ...removed])
  }, [follow, layout, world])

  const fadeGhost = useCallback((el: HTMLElement, key: string) => {
    play(el, [{ opacity: 1 }, { opacity: 0 }], fade, { fill: 'forwards' }).finished
      .then(() => setGhosts((current) => current.filter((ghost) => ghost.key !== key)), () => {})
  }, [])

  const fold = useCallback((unitKey: string, folded: boolean) => {
    folding.current.get(unitKey)?.forEach((animation) => animation.cancel())
    folding.current.delete(unitKey)
    const unit = [...world.current?.querySelectorAll<HTMLElement>('[data-map-unit]') ?? []].find((el) => el.dataset.glideKey === `u:${unitKey}`)
    const children = [...unit?.querySelectorAll<HTMLElement>('[data-map-role="child"]') ?? []]
    anchor.current = null
    if (!folded || !children.length || prefersReducedMotion()) {
      anchor.current = unitKey
      commit(unitKey, folded)
      return
    }
    const step = time('--stagger-row')
    const animations = children.map((el, index) => play(el, [{ opacity: 1 }, { opacity: 0 }], fade, {
      fill: 'forwards', extraDelay: Math.min(children.length - 1 - index, staggerCap) * step,
    }))
    const lines = unit?.querySelector('[data-map-connectors]')
    if (lines) animations.push(play(lines, [{ opacity: 1 }, { opacity: 0 }], fade, { fill: 'forwards' }))
    folding.current.set(unitKey, animations)
    Promise.all(animations.map((animation) => animation.finished)).then(() => {
      folding.current.delete(unitKey)
      for (const el of children) faded.current.add(el.dataset.glideKey!.slice(2))
      anchor.current = unitKey
      commit(unitKey, true)
    }, () => {})
  }, [commit, world])

  return { ghosts, fadeGhost, fold }
}
