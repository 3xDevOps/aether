import type { Member } from '@/lib/types'
import { cn } from '@/lib/utils'

/**
 * Members have no images, only a display name and the attribution colour the
 * core spec assigns them - so the avatar is their initials ringed in that
 * colour. The colour rings rather than fills because it is arbitrary server
 * data: text on top of it would have no contrast guarantee in either theme.
 */
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
        'flex size-5 shrink-0 items-center justify-center rounded-full border-2 text-[9px] font-medium',
        className,
      )}
    >
      {initials(name)}
    </span>
  )
}

function initials(name: string): string {
  const parts = name.trim().split(/[\s_-]+/).filter(Boolean)
  if (parts.length === 0) return '?'
  const first = parts[0][0]
  const last = parts.length > 1 ? parts[parts.length - 1][0] : ''
  return (first + last).toUpperCase()
}
