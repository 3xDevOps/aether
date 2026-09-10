import { Chip } from '@/components/ui/heroui'
import type { CardSlotProps } from '@/components/slots'
import { MemberAvatar } from '@/routes/board/member-avatar'
import { useStore } from '@/store'
import { onlineMembers, watchersOf } from '@/store/presence'

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
      className="flex items-center gap-1.5 rounded-md px-1.5 py-0.5"
      title={`Online: ${names.join(', ')}`}
      aria-label={`${online.length} online`}
    >
      <span className="flex items-center -space-x-1">
        {online.slice(0, shown).map((id) => (
          <MemberAvatar
            key={id}
            member={members[id]}
            fallback={id}
            className="size-5 bg-background text-[9px]"
          />
        ))}
      </span>
      <Chip color="success" variant="soft" size="sm">
        <Chip.Label>{online.length} online</Chip.Label>
      </Chip>
      {online.length > shown && (
        <Chip color="default" variant="tertiary" size="sm">
          <Chip.Label>+{online.length - shown}</Chip.Label>
        </Chip>
      )}
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
    <span className="flex items-center gap-1.5" title={`Watching: ${names.join(', ')}`}>
      <span className="flex items-center -space-x-1">
        {watchers.slice(0, shown).map((id) => (
          <MemberAvatar
            key={id}
            member={members[id]}
            fallback={id}
            className="size-4 bg-background text-[8px]"
          />
        ))}
      </span>
      {watchers.length > shown && (
        <Chip color="default" variant="tertiary" size="sm">
          <Chip.Label>+{watchers.length - shown}</Chip.Label>
        </Chip>
      )}
    </span>
  )
}
