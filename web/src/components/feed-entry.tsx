// Shared by the Activity view and a run's Raw events so the two feeds cannot drift.

import { memo } from 'react'
import { describeEvent } from '@/components/event-words'
import { Badge } from '@/components/ui/badge'
import { RelativeTime } from '@/components/ui/relative-time'
import { typeLabel } from '@/lib/events'
import { runLabel } from '@/lib/status'
import type { Event } from '@/lib/types'
import { cn, focusRing } from '@/lib/utils'
import { useStore } from '@/store'

export const FeedEntry = memo(function FeedEntry({ event, runLink = false }: { event: Event; runLink?: boolean }) {
  const actor = useStore((s) => s.members[event.actor_id])
  const run = useStore((s) => (runLink ? s.runs[event.run_id] : undefined))
  const navigate = useStore((s) => s.navigate)

  return (
    <li className="group grid min-w-0 grid-cols-[auto_minmax(0,1fr)] items-start gap-x-2 border-b border-seam px-3 py-2 text-ui transition-colors last:border-b-0 hover:bg-hover-chrome">
      <span
        role="img"
        aria-label={actor?.display_name ?? 'system'}
        title={actor?.display_name ?? 'system'}
        className="mt-1 size-2 shrink-0 rounded-full bg-icon-faint/45"
        style={actor ? { backgroundColor: actor.color } : undefined}
      />
      <div className="@container/feed-entry min-w-0">
        <div className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] items-start gap-x-2 gap-y-1 @md/feed-entry:grid-cols-[auto_minmax(0,1fr)_minmax(8rem,14rem)]">
          <span className="shrink-0 pt-px text-ui-sm tabular-nums text-muted">
            <RelativeTime at={event.time} title={event.time} />
          </span>
          <span title={event.type} className="min-w-0 max-w-full">
            <Badge className="min-w-0 max-w-full justify-self-start">
              <span className="truncate">{typeLabel(event.type)}</span>
            </Badge>
          </span>
          <span className="col-start-2 min-w-0 break-words leading-5 text-text/90 select-text">
            {describeEvent(event)}
          </span>
          {run && (
            <button
              type="button"
              onClick={() => navigate('run', { runId: run.id })}
              aria-label={runLabel(run)}
              title={runLabel(run)}
              className={cn(
                focusRing,
                'col-start-2 row-start-3 min-h-[26px] coarse:min-h-11 min-w-0 max-w-full justify-self-start break-words text-left text-ui-sm text-muted hover:text-text hover:underline @md/feed-entry:col-start-3 @md/feed-entry:row-start-1 @md/feed-entry:row-span-2 @md/feed-entry:justify-self-end',
              )}
            >
              <span className="line-clamp-2">{runLabel(run)}</span>
            </button>
          )}
        </div>
      </div>
    </li>
  )
})
