import { useMemo, useRef } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { EmptyState } from '@/components/ui/empty-state'
import { Skeleton } from '@/components/ui/skeleton'
import type { Api } from '@/lib/api'
import type { Event } from '@/lib/types'
import { EventRow, VisitRow } from '@/routes/activity/event-row'
import { VirtualList } from '@/routes/activity/virtual-list'
import { type FeedItem, foldVisits } from '@/routes/activity/visits'
import { olderFeed, pageBudget } from '@/routes/team/sync'
import { useStore } from '@/store'

export function feedItems(feed: readonly Event[], raw: boolean): FeedItem[] {
  return raw ? feed.map((event) => ({ kind: 'event', event })) : foldVisits(feed)
}

export function EventFeed({ client, raw }: { client: Api; raw: boolean }) {
  const feed = useStore((s) => s.feed)
  const older = useStore((s) => s.feedOlder)
  const loading = useStore((s) => s.feedLoading)
  const error = useStore((s) => s.feedError)
  const truncated = useStore((s) => s.feedTruncated)
  const newestFirst = useMemo(() => feedItems(feed, raw).reverse(), [feed, raw])
  const scroller = useRef<HTMLDivElement>(null)

  return (
    <>
    {error && <Callout tone="failed" role="alert" className="m-3">{error}</Callout>}
    <div ref={scroller} role="region" aria-label="Activity feed" className="min-h-0 flex-1 overflow-y-auto">
      {loading && feed.length === 0 && !error && (
        <div aria-label="Loading activity" className="flex flex-col gap-2 p-4">
          {[0, 1, 2].map((row) => <div key={row} className="h-5"><Skeleton className="size-full" /></div>)}
        </div>
      )}
      {feed.length === 0 && !loading && !error && (
        <EmptyState title="Nothing here yet">Activity appears here as your team works in this workspace.</EmptyState>
      )}
      <VirtualList data={newestFirst} scrollRef={scroller}>
        {(item) => (item.kind === 'visit' ? <VisitRow key={item.id} visit={item} /> : <EventRow key={item.event.id} event={item.event} raw={raw} />)}
      </VirtualList>
      {(truncated || older) && (
        <div className="flex flex-col items-start gap-2 px-4 py-3">
          {truncated && (
            <p className="text-ui-sm text-muted">
              Stopped after {pageBudget} entries, so part of this stretch of history is not shown. Narrow the filters to see it.
            </p>
          )}
          {older && (
            <Button variant="secondary" size="sm" disabled={loading} onClick={() => void olderFeed(useStore, client)}>
              Load older
            </Button>
          )}
        </div>
      )}
    </div>
    </>
  )
}
