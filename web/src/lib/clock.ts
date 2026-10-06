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

/** Returns the last tick time. Compute text from the real time instead, so a
 * row mounted between ticks is not stale. */
export function useClock(): number {
  return useSyncExternalStore(subscribe, snapshot, snapshot)
}
