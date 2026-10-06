import { Badge } from '@/components/ui/badge'
import type { CardSlotProps } from '@/components/slots'
import { MemberAvatar } from '@/routes/board/member-avatar'
import { useStore } from '@/store'
import { watchersOf } from '@/store/presence'

/** How many avatars a row shows before it collapses into a count. */
const shown = 4

/** The watcher avatars on a run card: who holds an attach on this run. */
export function Watchers({ run }: CardSlotProps) {
  const presence = useStore((s) => s.presence)
  const members = useStore((s) => s.members)
  const watchers = watchersOf(presence, run.id)
  if (watchers.length === 0) return null

  const names = watchers.map((id) => members[id]?.display_name ?? id)
  return (
    <span className="flex min-w-0 shrink-0 items-center gap-1.5" title={`Watching: ${names.join(', ')}`}>
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
        <Badge>
          +{watchers.length - shown}
        </Badge>
      )}
    </span>
  )
}
