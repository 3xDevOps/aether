import type { StateCreator, StoreMutatorIdentifier } from 'zustand'
import type { RootStore } from '@/store'

type Listener<T> = (state: T, previous: T) => void
type Mutators = [StoreMutatorIdentifier, unknown][]

interface Batch {
  begin: () => void
  end: () => void
}

/**
 * Keyed by the wrapped `subscribe`, the one member the store hook and its
 * vanilla API share by reference: `create` copies the API onto the hook.
 */
const batches = new WeakMap<object, Batch>()

/**
 * Store middleware that can hold subscriber notification back while
 * `batchNotifications` runs. `set()` still applies at once, so `getState()`
 * is always current; only listeners wait. They hear one change - the state
 * before the first held write and the state now - on the next frame. The
 * hold outlives the batch: each socket message drains in its own task, so
 * ending on an empty queue would notify once per message of a burst. A write
 * outside any batch, which is the user acting, notifies at once and takes
 * the held change with it.
 */
export function batched<
  T,
  Mps extends Mutators = [],
  Mcs extends Mutators = [],
>(config: StateCreator<T, Mps, Mcs>): StateCreator<T, Mps, Mcs> {
  return (set, get, api) => {
    const listeners = new Set<Listener<T>>()
    let depth = 0
    let held: { previous: T } | null = null
    let cancelFrame: (() => void) | null = null

    const notify = (state: T, previous: T) => {
      for (const listener of listeners) listener(state, previous)
    }
    const flush = () => {
      cancelFrame?.()
      cancelFrame = null
      if (!held) return
      const { previous } = held
      held = null
      notify(api.getState(), previous)
    }
    // A hidden tab runs no frames, so the timer stands in for one there.
    const schedule = () => {
      if (cancelFrame) return
      const timer = setTimeout(flush, 16)
      const frame = typeof requestAnimationFrame === 'function' ? requestAnimationFrame(flush) : null
      cancelFrame = () => {
        clearTimeout(timer)
        if (frame !== null) cancelAnimationFrame(frame)
      }
    }

    api.subscribe((state, previous) => {
      if (depth === 0) {
        if (held) flush()
        else notify(state, previous)
        return
      }
      held ??= { previous }
      schedule()
    })
    api.subscribe = (listener) => {
      listeners.add(listener)
      return () => listeners.delete(listener)
    }
    batches.set(api.subscribe, {
      begin: () => {
        depth++
      },
      end: () => {
        depth--
      },
    })
    return config(set, get, api)
  }
}

/** Runs `fn` with the store's notifications held back until it settles. */
export async function batchNotifications<R>(store: RootStore, fn: () => Promise<R>): Promise<R> {
  const batch = batches.get(store.subscribe)
  batch?.begin()
  try {
    return await fn()
  } finally {
    batch?.end()
  }
}
