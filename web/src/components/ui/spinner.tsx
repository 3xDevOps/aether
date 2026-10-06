import { LoaderCircle } from '@/components/icons'
import { cn } from '@/lib/utils'

export function Spinner({ label, className }: { label?: string; className?: string }) {
  return (
    <LoaderCircle
      data-slot="spinner"
      {...(label ? { role: 'status', 'aria-label': label } : { 'aria-hidden': true })}
      className={cn('size-3.5 shrink-0 animate-spin text-muted motion-reduce:animate-none', className)}
    />
  )
}
