import { initials } from '@/components/ui/avatar'
import type { Member } from '@/lib/types'
import { cn } from '@/lib/utils'

/** The colour rings rather than fills: it is arbitrary server data, so text on it has no contrast guarantee. */
export function MemberAvatar({
  member,
  fallback,
  className,
}: {
  member?: Member
  fallback?: string
  className?: string
}) {
  const name = member?.display_name ?? fallback ?? '?'
  return (
    <span
      role="img"
      aria-label={name}
      title={name}
      style={member ? { borderColor: member.color } : undefined}
      className={cn(
        'flex size-5 shrink-0 items-center justify-center rounded-full border-2 bg-sidebar text-[9px] font-medium',
        className,
      )}
    >
      {initials(name)}
    </span>
  )
}
