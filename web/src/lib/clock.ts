import { useSyncExternalStore } from 'react'

/** Relative times read in minutes, so a half-minute tick keeps them honest. */
const tickMs = 30_000

let tick = Date.now()
const listeners = new Set<() => void>()
let timer: ReturnType<typeof setInterval> | null = null

function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  timer ??= setInterval(() => {
    tick = Date.now()
    for (const notify of listeners) notify()
  }, tickMs)
  return () => {
    listeners.delete(listener)
    if (listeners.size > 0 || !timer) return
    clearInterval(timer)
    timer = null
  }
}

const snapshot = () => tick

/**
 * Re-renders the caller every 30 seconds, from one interval shared by every
 * subscriber. The value is when the clock last ticked; text should still be
 * computed from the real time, so a row mounted between ticks is not stale.
 */
export function useClock(): number {
  return useSyncExternalStore(subscribe, snapshot, snapshot)
}
