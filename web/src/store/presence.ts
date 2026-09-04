import type { PresenceEntry } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

export interface PresenceSlice {
  /**
   * The live roster. Presence is per workspace, so one member appears once
   * per workspace they are present in. Offline entries remain until a later
   * live roster entry replaces them, preserving their last_seen timestamp.
   */
  presence: PresenceEntry[]
  setPresence: (entries: PresenceEntry[]) => void
}

export const createPresenceSlice: SliceCreator<PresenceSlice> = (set) => ({
  presence: [],
  setPresence: (entries) =>
    set((state) => {
      const liveMembers = new Set(entries.map((entry) => entry.member_id))
      const retained = new Map<string, PresenceEntry>()
      for (const entry of state.presence) {
        if (!liveMembers.has(entry.member_id) && !retained.has(entry.member_id)) {
          retained.set(entry.member_id, {
            ...entry,
            state: 'offline',
            watching: undefined,
          })
        }
      }
      return { presence: [...entries, ...retained.values()] }
    }),
})

/** Who is online anywhere, deduplicated across workspaces. */
export function onlineMembers(presence: PresenceEntry[]): string[] {
  return [
    ...new Set(
      presence.filter((p) => p.state !== 'offline').map((p) => p.member_id),
    ),
  ].sort()
}

/** Who currently holds an attach on a run. */
export function watchersOf(presence: PresenceEntry[], runID: string): string[] {
  return [
    ...new Set(
      presence
        .filter((p) => p.state !== 'offline' && p.watching?.includes(runID))
        .map((p) => p.member_id),
    ),
  ].sort()
}
