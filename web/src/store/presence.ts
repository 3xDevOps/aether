import type { PresenceEntry } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

export interface PresenceSlice {
  /**
   * The live roster. Presence is per workspace, so one member appears once
   * per workspace they are present in.
   */
  presence: PresenceEntry[]
  setPresence: (entries: PresenceEntry[]) => void
}

export const createPresenceSlice: SliceCreator<PresenceSlice> = (set) => ({
  presence: [],
  setPresence: (presence) => set({ presence }),
})

/** Who is online anywhere, deduplicated across workspaces. */
export function onlineMembers(presence: PresenceEntry[]): string[] {
  return [...new Set(presence.map((p) => p.member_id))].sort()
}

/** Who currently holds an attach on a run. */
export function watchersOf(presence: PresenceEntry[], runID: string): string[] {
  return [
    ...new Set(
      presence.filter((p) => p.watching?.includes(runID)).map((p) => p.member_id),
    ),
  ].sort()
}
