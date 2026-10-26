import { Skeleton } from '@/components/ui/skeleton'
import { useDelayed } from '@/lib/hooks'
import { cn } from '@/lib/utils'

const widths = ['w-3/4', 'w-1/2', 'w-5/6', 'w-2/5', 'w-2/3', 'w-3/5', 'w-1/3', 'w-4/5', 'w-1/2', 'w-2/3']

/** A file header and ten lines of code, shown once a diff has taken a moment to arrive. */
export function DiffSkeleton({ className }: { className?: string }) {
  const shown = useDelayed(true)
  if (!shown) return null
  return (
    <div role="status" className={cn('flex flex-col gap-3 p-4', className)}>
      <span className="sr-only">Loading the diff…</span>
      <Skeleton className="h-8" />
      <div className="flex flex-col gap-2">
        {widths.map((width, line) => <Skeleton key={line} className={cn('h-4', width)} />)}
      </div>
    </div>
  )
}
