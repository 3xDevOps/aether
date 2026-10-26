import { useSyncExternalStore } from 'react'

function ticker(ms: number) {
  let tick = Date.now()
  const listeners = new Set<() => void>()
  let timer: ReturnType<typeof setInterval> | null = null
  return {
    subscribe(listener: () => void): () => void {
      listeners.add(listener)
      if (!timer) tick = Date.now()
      timer ??= setInterval(() => {
        tick = Date.now()
        for (const notify of listeners) notify()
      }, ms)
      return () => {
        listeners.delete(listener)
        if (listeners.size > 0 || !timer) return
        clearInterval(timer)
        timer = null
      }
    },
    snapshot: () => tick,
  }
}

/** Relative times read in minutes, so a half-minute tick keeps them honest. */
const halfMinute = ticker(30_000)
const second = ticker(1000)
const idle = () => () => {}

/** Returns the last tick time. Compute text from the real time instead, so a
 * row mounted between ticks is not stale. */
export function useClock(): number {
  return useSyncExternalStore(halfMinute.subscribe, halfMinute.snapshot, halfMinute.snapshot)
}

/** A once-a-second tick for a live elapsed time; it holds still while inactive. */
export function useSecondClock(active: boolean): number {
  return useSyncExternalStore(active ? second.subscribe : idle, second.snapshot, second.snapshot)
}
