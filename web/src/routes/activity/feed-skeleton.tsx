import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'

const widths = ['w-2/3', 'w-1/2', 'w-3/4', 'w-2/5', 'w-3/5', 'w-1/2', 'w-2/3', 'w-1/3']

/** Rows shaped like the feed's: time, state dot, avatar, then the words. */
export function FeedSkeleton({ label }: { label: string }) {
  return (
    <div role="status" aria-label={label} className="flex flex-col py-1.5">
      {widths.map((width, row) => (
        <div key={row} className="flex h-8 items-center gap-2 px-4">
          <div className="w-24 shrink-0">
            <Skeleton className="h-3 w-14" />
          </div>
          <Skeleton className="size-2.5 shrink-0 rounded-full" />
          <Skeleton className="size-4 shrink-0 rounded-full" />
          <Skeleton className={cn('h-3', width)} />
        </div>
      ))}
    </div>
  )
}
