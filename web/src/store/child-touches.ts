import { create } from 'zustand'

/** Counts, per run, the events that may change its child list without a run snapshot changing: reservations and spend. */
export const useChildTouches = create<{ touched: Record<string, number> }>(() => ({ touched: {} }))

export function touchChildren(...runIDs: (string | undefined)[]) {
  useChildTouches.setState((s) => {
    const touched = { ...s.touched }
    for (const id of runIDs) if (id) touched[id] = (touched[id] ?? 0) + 1
    return { touched }
  })
}
