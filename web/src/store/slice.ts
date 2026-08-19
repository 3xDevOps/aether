import type { StateCreator } from 'zustand'
import type { RootState } from '@/store'

/**
 * Every slice is written against the whole root state, so a slice may read
 * another slice's data. Add a slice by writing one of these in its own file
 * and spreading it in `createRootStore` - nothing else changes.
 */
export type SliceCreator<T> = StateCreator<
  RootState,
  [['zustand/persist', unknown]],
  [],
  T
>
