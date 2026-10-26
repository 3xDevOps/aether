import { useSyncExternalStore } from 'react'

function ticker(tickMs: number) {
  let tick = Date.now()
  const listeners = new Set<() => void>()
  let timer: ReturnType<typeof setInterval> | null = null

  const subscribe = (listener: () => void): (() => void) => {
    listeners.add(listener)
    if (!timer) tick = Date.now()
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
  return () => useSyncExternalStore(subscribe, snapshot, snapshot)
}

/** Returns the last tick time. Compute text from the real time instead, so a
 * row mounted between ticks is not stale. Relative times read in minutes, so
 * a half-minute tick keeps them honest. */
export const useClock = ticker(30_000)

export const useSecondClock = ticker(1000)
