import { MemberAvatar } from '@/routes/board/member-avatar'
import { useStore } from '@/store'
import { onlineMembers, watchersOf } from '@/store/presence'
import type { CardSlotProps } from '@/components/slots'

/** How many avatars a row shows before it collapses into a count. */
const shown = 4

/** Who is online, in the status bar. */
export function PresenceStatus() {
  const presence = useStore((s) => s.presence)
  const members = useStore((s) => s.members)
  const online = onlineMembers(presence)
  if (online.length === 0) return null

  const names = online.map((id) => members[id]?.display_name ?? id)
  return (
    <span
      className="flex items-center gap-1"
      title={`Online: ${names.join(', ')}`}
      aria-label={`${online.length} online`}
    >
      {online.slice(0, shown).map((id) => (
        <MemberAvatar key={id} member={members[id]} fallback={id} />
      ))}
      {online.length > shown && <span>+{online.length - shown}</span>}
    </span>
  )
}

/** The watcher avatars on a run card: who holds an attach on this run. */
export function Watchers({ run }: CardSlotProps) {
  const presence = useStore((s) => s.presence)
  const members = useStore((s) => s.members)
  const watchers = watchersOf(presence, run.id)
  if (watchers.length === 0) return null

  const names = watchers.map((id) => members[id]?.display_name ?? id)
  return (
    <span className="flex items-center gap-1" title={`Watching: ${names.join(', ')}`}>
      {watchers.slice(0, shown).map((id) => (
        <MemberAvatar
          key={id}
          member={members[id]}
          fallback={id}
          className="size-4 text-[8px]"
        />
      ))}
      {watchers.length > shown && (
        <span className="text-[10px]">+{watchers.length - shown}</span>
      )}
    </span>
  )
}
