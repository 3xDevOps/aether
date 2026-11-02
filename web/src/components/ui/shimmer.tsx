import { cn } from '@/lib/utils'

/** Muted text that a light band sweeps across while the work it names is live. */
export function Shimmer({ text, className }: { text: string; className?: string }) {
  return (
    <span className={cn('live-shimmer', className)}>
      {text}
      <span aria-hidden className="live-shimmer-band" data-text={text} />
    </span>
  )
}
