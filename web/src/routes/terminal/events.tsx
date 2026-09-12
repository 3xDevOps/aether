// The run detail's Events tab: the shared workspace feed pinned to one run.
// It drives the same store feed slice and readers the team activity view
// uses, so the window, live tail and page budget behave identically. The
// pin is borrowed, not kept: unmounting hands the filters back, so the
// activity view opens with whatever it had chosen.

import { useEffect } from 'react'
import { FeedEntry } from '@/components/feed-entry'
import { MissingRun } from '@/components/missing-run'
import { RunHeader } from '@/components/run-header'
import { Button } from '@/components/ui/button'
import { api, type Api } from '@/lib/api'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { drain, olderFeed, openFeed, pageBudget } from '@/routes/team/sync'
import { runTabPanel } from '@/routes/terminal/tabs'
import { useStore } from '@/store'

export function RunEvents({ params, client = api }: RouteProps & { client?: Api }) {
  const runID = params.runId
  const run = useStore((s) => s.runs[runID])
  const filters = useStore((s) => s.feedFilters)
  const setFilters = useStore((s) => s.setFeedFilters)
  const feed = useStore((s) => s.feed)
  const older = useStore((s) => s.feedOlder)
  const loading = useStore((s) => s.feedLoading)
  const error = useStore((s) => s.feedError)
  const truncated = useStore((s) => s.feedTruncated)
  const lastSeq = useStore((s) => s.lastSeq)

  const workspaceID = run?.workspace_id ?? ''
  const pinned = filters.workspaceID === workspaceID && filters.runID === runID

  // Runs before the pin below, so it captures the filters the activity view
  // chose and restores them when this tab lets go of the shared feed.
  useEffect(() => {
    const prev = useStore.getState().feedFilters
    return () => useStore.getState().setFeedFilters(prev)
  }, [])

  useEffect(() => {
    if (workspaceID && !pinned) {
      setFilters({ workspaceID, runID, memberID: '', type: '' })
    }
  }, [workspaceID, runID, pinned, setFilters])

  useEffect(() => {
    if (pinned) void openFeed(useStore, client)
  }, [pinned, filters, client])

  // Live tail: every applied event moves the cursor, and whatever landed
  // after ours is one page away.
  useEffect(() => {
    if (pinned && !useStore.getState().feedLoading) void drain(useStore, client)
  }, [pinned, lastSeq, client])

  if (!run) {
    return <MissingRun />
  }

  return (
    <div className="flex h-full min-w-0 flex-col overflow-hidden">
      <RunHeader run={run} subtitle={run.branch} active="events" />
      <div
        {...runTabPanel('events', 'min-h-0 min-w-0 flex-1 overflow-y-auto bg-background p-3 sm:p-4', true)}
      >
        {error && (
          <p
            role="alert"
            className="mb-4 min-w-0 break-words whitespace-pre-wrap border-l-2 border-state-failed bg-state-failed/10 px-3 py-2 text-[13px] leading-5 text-state-failed"
          >
            {error}
          </p>
        )}
        <ol className="space-y-0">
          {[...feed].reverse().map((event) => (
            <FeedEntry key={event.id} event={event} />
          ))}
        </ol>
        {feed.length === 0 && !loading && (
          <p className="border border-dashed border-border px-4 py-6 text-center text-[13px] text-muted-foreground">
            Nothing here yet.
          </p>
        )}
        {truncated && (
          <p className="mt-3 border-l-2 border-border bg-sidebar px-3 py-2 text-[13px] leading-5 text-muted-foreground">
            Stopped after {pageBudget} entries, so part of this stretch of
            history is not shown.
          </p>
        )}
        {older && (
          <Button
            variant="ghost"
            size="sm"
            className="mt-3 shrink-0"
            disabled={loading}
            onClick={() => void olderFeed(useStore, client)}
          >
            Load older
          </Button>
        )}
      </div>
    </div>
  )
}

registerRoute('events', RunEvents)
