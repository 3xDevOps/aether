import type { SliceCreator } from '@/store/slice'

/** Longer than the sender's throttle, so a member who keeps typing never flickers off. */
export const typingTtlMs = 6_000
export const typingThrottleMs = 3_000

interface Typer {
  since: number
  until: number
}

export interface TypingSlice {
  /** Ephemeral: who is typing in each run's composer. Never persisted or counted. */
  typing: Record<string, Record<string, Typer>>
  noteTyping: (runID: string, memberID: string, now?: number) => void
  /** Without a member, the whole run's entries go. */
  clearTyping: (runID: string, memberID?: string) => void
}

export const createTypingSlice: SliceCreator<TypingSlice> = (set, get) => ({
  typing: {},
  noteTyping: (runID, memberID, now = Date.now()) =>
    set((s) => {
      const current = s.typing[runID] ?? {}
      const held = current[memberID]
      const live = Object.fromEntries(Object.entries(current).filter(([, t]) => t.until > now))
      live[memberID] = { since: held && held.until > now ? held.since : now, until: now + typingTtlMs }
      return { typing: { ...s.typing, [runID]: live } }
    }),
  clearTyping: (runID, memberID) => {
    const current = get().typing[runID]
    if (!current || (memberID !== undefined && !current[memberID])) return
    set((s) => {
      const kept = memberID === undefined ? [] : Object.entries(current).filter(([id]) => id !== memberID)
      const typing = { ...s.typing }
      if (kept.length) typing[runID] = Object.fromEntries(kept)
      else delete typing[runID]
      return { typing }
    })
  },
})

/** Who is typing at `now`, longest-typing first, never `selfID`. */
export function activeTypers(entries: Record<string, Typer> | undefined, now: number, selfID: string): string[] {
  return Object.entries(entries ?? {})
    .filter(([id, t]) => id !== selfID && t.until > now)
    .sort(([a, x], [b, y]) => x.since - y.since || a.localeCompare(b))
    .map(([id]) => id)
}

/** Discord's wording: up to three names, then "Several people". Empty for nobody. */
export function typingLabel(names: string[]): string {
  switch (names.length) {
    case 0:
      return ''
    case 1:
      return `${names[0]} is typing`
    case 2:
      return `${names[0]} and ${names[1]} are typing`
    case 3:
      return `${names[0]}, ${names[1]} and ${names[2]} are typing`
    default:
      return 'Several people are typing'
  }
}

/** The moment the first of these entries expires, undefined for none. */
export function nextTypingExpiry(entries: Record<string, Typer> | undefined, now: number): number | undefined {
  const pending = Object.values(entries ?? {}).map((t) => t.until).filter((until) => until > now)
  return pending.length ? Math.min(...pending) : undefined
}
