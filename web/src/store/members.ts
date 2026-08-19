import type { Member } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

export interface MembersSlice {
  members: Record<string, Member>
  setMembers: (members: Member[]) => void
}

export const createMembersSlice: SliceCreator<MembersSlice> = (set) => ({
  members: {},
  setMembers: (members) =>
    set({ members: Object.fromEntries(members.map((m) => [m.id, m])) }),
})
