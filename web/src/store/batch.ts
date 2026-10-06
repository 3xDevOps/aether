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
 * before the first held write and the state now - when the batch ends or a
 * frame has passed, whichever comes first.
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
    const schedule = () => {
      if (cancelFrame) return
      if (typeof requestAnimationFrame === 'function') {
        const id = requestAnimationFrame(flush)
        cancelFrame = () => cancelAnimationFrame(id)
      } else {
        const id = setTimeout(flush, 16)
        cancelFrame = () => clearTimeout(id)
      }
    }

    api.subscribe((state, previous) => {
      if (depth === 0) {
        notify(state, previous)
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
        if (depth === 0) flush()
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
