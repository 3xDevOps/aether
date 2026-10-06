import { cn } from '@/lib/utils'

export function initials(name: string): string {
  const parts = name.trim().split(/[\s_-]+/).filter(Boolean)
  if (parts.length === 0) return '?'
  const first = parts[0][0]
  const last = parts.length > 1 ? parts[parts.length - 1][0] : ''
  return (first + last).toUpperCase()
}

export function Avatar({
  name,
  color,
  size = 'row',
  className,
}: {
  name: string
  color?: string
  size?: 'row' | 'header'
  className?: string
}) {
  const letters = initials(name)
  return (
    <span
      data-slot="avatar"
      role="img"
      aria-label={name}
      title={name}
      style={color ? { borderColor: color } : undefined}
      className={cn(
        'inline-flex shrink-0 items-center justify-center rounded-full border-[1.5px] border-seam bg-chrome text-avatar font-medium text-text',
        size === 'row' ? 'size-4' : 'size-5',
        className,
      )}
    >
      {size === 'row' ? letters[0] : letters}
    </span>
  )
}
