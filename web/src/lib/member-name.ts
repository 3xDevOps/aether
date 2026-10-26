import type { Member } from '@/lib/types'
import type { RootState } from '@/store'

type Names = Pick<RootState, 'members' | 'mergedInto'>

/** The member an id names, following merges to the member it is now. */
export function resolveMember(s: Names, id: string): { member?: Member; merged: boolean } {
  let current = id
  for (let hops = 0; s.mergedInto[current] && hops < 16; hops++) current = s.mergedInto[current]
  return { member: s.members[current], merged: current !== id }
}

/** The display name for an id history names: a merged-away id shows the
 * member it became, marked merged; an unknown id shows itself. */
export function memberName(s: Names, id: string): string {
  const { member, merged } = resolveMember(s, id)
  if (!member) return id
  return merged ? `${member.display_name} (merged)` : member.display_name
}

/** `memberName`, but an id with no member record shows `snapshot`, the name the message carried, before the raw id. */
export function memberNameOr(s: Names, id: string, snapshot?: string): string {
  return resolveMember(s, id).member ? memberName(s, id) : snapshot || id
}
