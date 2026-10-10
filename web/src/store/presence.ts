import type { Member, PresenceEntry, Run } from '@/lib/types'
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

/** Members attached to a run, once each: the roster has a row per workspace. */
export function runWatchers(presence: PresenceEntry[], runID: string): string[] {
  return [...new Set(presence.filter((p) => p.watching?.includes(runID)).map((p) => p.member_id))]
}

/**
 * Everyone on a run, the member to show first leading: its controller, else
 * its last controller while still attached. The rest follow by name.
 */
export function runPeople(
  run: Pick<Run, 'controller_member_id' | 'last_controller_member_id'>,
  watchers: string[],
  members: Record<string, Member>,
): string[] {
  const last = run.last_controller_member_id
  const lead = run.controller_member_id || (last && watchers.includes(last) ? last : undefined)
  const name = (id: string) => members[id]?.display_name ?? id
  const rest = watchers
    .filter((id) => id !== lead)
    .sort((a, b) => name(a).localeCompare(name(b)) || a.localeCompare(b))
  return lead ? [lead, ...rest] : rest
}

/** Who is online anywhere, deduplicated across workspaces. */
export function onlineMembers(presence: PresenceEntry[]): string[] {
  return [
    ...new Set(
      presence.filter((p) => p.state !== 'offline').map((p) => p.member_id),
    ),
  ].sort()
}
