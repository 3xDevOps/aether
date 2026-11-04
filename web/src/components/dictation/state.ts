import { useSyncExternalStore, type RefObject } from 'react'

type Box = RefObject<HTMLTextAreaElement | null>

export interface DictationState {
  /** Starting the microphone, recording or transcribing: the box's send waits. */
  active: boolean
  /** `text` is verbatim; `hint` is ours, shown apart from it. */
  notice: { text: string; error: boolean; hint?: string } | null
}

const idle: DictationState = { active: false, notice: null }
const states = new Map<Box, DictationState>()
const listeners = new Set<() => void>()

/** Keyed by the box's ref, so the button and the composer around it share one state without a prop. */
export function setDictationState(box: Box, next: Partial<DictationState>) {
  const current = states.get(box) ?? idle
  const merged = { ...current, ...next }
  if (!merged.active && !merged.notice) states.delete(box)
  else states.set(box, merged)
  for (const listener of listeners) listener()
}

export function useDictationState(box: Box): DictationState {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    () => states.get(box) ?? idle,
  )
}
