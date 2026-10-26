import { cn } from '@/lib/utils'

export function Spinner({ label, className }: { label?: string; className?: string }) {
  return (
    <svg
      data-slot="spinner"
      viewBox="0 0 14 14"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      {...(label ? { role: 'status', 'aria-label': label } : { 'aria-hidden': true })}
      className={cn('size-3.5 shrink-0 animate-spin text-muted motion-reduce:animate-none', className)}
    >
      <circle cx="7" cy="7" r="6.25" className="opacity-track" />
      <circle cx="7" cy="7" r="6.25" pathLength={360} strokeDasharray="90 360" strokeLinecap="round" transform="rotate(-90 7 7)" />
    </svg>
  )
}
