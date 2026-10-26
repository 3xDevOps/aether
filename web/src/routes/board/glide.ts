import { useCallback, useLayoutEffect, useRef, type RefObject } from 'react'
import { capture, glideFrom, type Box } from '@/lib/flip'
import { playEntrance, type Motion } from '@/lib/motion'
import type { BoardColumn } from '@/routes/board/selectors'
import { useStore } from '@/store'
import type { RunRecord } from '@/store/runs'

const glide: Motion = { duration: '--duration-glide', easing: '--ease-glide' }

/** More cards than this changing column at once is a reload, not news. */
export const glideLimit = 8

export interface Placement {
  /** Workspace, Mine, archive and layout: a change of any is a new board, not a move. */
  scope: string
  /** `runSnapshots`: hydration and a reconnect re-read every run, however few move. */
  snapshot: number
  runs: Record<string, RunRecord>
  columns: Map<string, BoardColumn['key']>
}

/** Whether going from `before` to `after` moves a few cards between columns as live news. */
export function isLiveMove(before: Placement, after: Placement): boolean {
  if (before.scope !== after.scope || before.snapshot !== after.snapshot) return false
  let moved = 0
  for (const [id, column] of after.columns) {
    const was = before.columns.get(id)
    if (was !== undefined && was !== column) moved++
  }
  if (moved === 0 || moved > glideLimit) return false
  return Object.keys(before.runs).filter((id) => after.runs[id] !== before.runs[id]).length <= glideLimit
}

/**
 * `useLayoutGlide` for the board's cards. Each Board column clips its own
 * scroll, so a card that lands in another column cannot travel there: it
 * glides only vertically and fades in. In List every card shares one scroll
 * and glides the whole way.
 */
export function useCardGlide(container: RefObject<HTMLElement | null>, layout: 'board' | 'list' | 'map'): () => void {
  const pending = useRef<Map<string, Box> | null>(null)
  useLayoutEffect(() => {
    const boxes = pending.current
    if (!boxes) return
    pending.current = null
    container.current?.querySelectorAll<HTMLElement>('[data-glide-key]').forEach((el) => {
      const from = boxes.get(el.dataset.glideKey!)
      if (!from) return
      const now = el.getBoundingClientRect()
      if (layout !== 'board' || Math.abs(now.left - from.left) < 1) {
        glideFrom(el, from, glide)
        return
      }
      glideFrom(el, { left: now.left, top: from.top }, glide)
      playEntrance(el, { motion: glide })
    })
  })
  return useCallback(() => {
    pending.current = capture(container.current)
  }, [container])
}

/** Glides the cards a live update moves between columns, and the cards they displace. */
export function useMoveGlide(capture: () => void, columns: BoardColumn[], scope: string) {
  const runs = useStore((s) => s.runs)
  const snapshot = useStore((s) => s.runSnapshots)
  const last = useRef<Placement | null>(null)
  const now: Placement = {
    scope,
    snapshot,
    runs,
    columns: new Map(columns.flatMap((column) => column.cards.map((card) => [card.run.id, column.key] as const))),
  }
  // Rendering is the last moment the DOM still shows the old columns: a store
  // update has no handler that runs before it commits.
  if (last.current && isLiveMove(last.current, now)) capture()
  last.current = now
}
