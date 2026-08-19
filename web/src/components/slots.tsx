// Extension slots. A feature that needs to put something inside a surface it
// does not own registers a component into a named slot instead of editing the
// surface. Same shape as the route registry: register at module scope, get
// imported once for the side effect.

import type { ComponentType, ReactNode } from 'react'
import type { RunRecord } from '@/store/runs'

/** What a run card hands the components rendered inside it. */
export interface CardSlotProps {
  run: RunRecord
}

/** Every slot and the props its contributors receive. */
export interface SlotPropsMap {
  /** Compact markers on the card's title row: paused, protected, approvals. */
  'card:badges': CardSlotProps
  /** A wrapping row under the task line: conflict chips and the like. */
  'card:chips': CardSlotProps
  /** The card's bottom row, right of the owner: watcher avatars. */
  'card:footer': CardSlotProps
  /** The status bar, left of the theme toggle. */
  statusbar: Record<never, never>
}

export type SlotName = keyof SlotPropsMap
/** The slots that live inside a run card and take its run. */
export type CardSlotName = 'card:badges' | 'card:chips' | 'card:footer'

type AnyProps = Record<string, unknown>
type Entry = { id: string; view: ComponentType<AnyProps> }

const registry = new Map<SlotName, Entry[]>()

/**
 * Adds a component to a slot. `id` names the contributor - it keys the render
 * and makes a double registration an error rather than a duplicate.
 */
export function registerSlot<N extends SlotName>(
  name: N,
  id: string,
  view: ComponentType<SlotPropsMap[N]>,
): void {
  const entries = registry.get(name) ?? []
  if (entries.some((e) => e.id === id)) {
    throw new Error(`slot already registered: ${name}/${id}`)
  }
  entries.push({ id, view: view as ComponentType<AnyProps> })
  registry.set(name, entries)
}

/** Renders everything registered into a slot, in registration order. */
export function Slot<N extends SlotName>({
  name,
  ...props
}: { name: N } & SlotPropsMap[N]): ReactNode {
  const entries = registry.get(name)
  if (!entries?.length) return null
  return entries.map(({ id, view: View }) => (
    <View key={id} {...(props as AnyProps)} />
  ))
}
