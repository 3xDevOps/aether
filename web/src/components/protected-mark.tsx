import { Lock } from '@/components/icons'
import { cn } from '@/lib/utils'

const text = 'Protected: only its owner or an admin can steer, message or close it'

export function ProtectedMark({ className }: { className?: string }) {
  return <Lock role="img" aria-label={text} className={cn('size-3 shrink-0 text-muted', className)}><title>{text}</title></Lock>
}
