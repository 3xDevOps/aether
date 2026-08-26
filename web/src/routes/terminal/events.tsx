// The run detail's Events tab: the shared workspace feed pinned to one run.
// It drives the same store feed slice and readers the team activity view
// uses, so the window, live tail and page budget behave identically. The
// pin is borrowed, not kept: unmounting hands the filters back, so the
// activity view opens with whatever it had chosen.

import { useEffect } from 'react'
import { FeedEntry } from '@/components/feed-entry'
import { Button } from '@/components/ui/button'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { drain, olderFeed, openFeed, pageBudget } from '@/routes/team/sync'
import { RunTabs } from '@/routes/terminal/tabs'
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
    return <p className="p-4 text-sm text-muted-foreground">Unknown run.</p>
  }

  return (
    <div className="flex h-full flex-col">
      <ViewHeader title={run.task} subtitle={run.branch} />
      <RunTabs runID={runID} active="events" />
      <div className="flex-1 overflow-y-auto p-3">
        {error && <p className="mb-2 text-xs text-state-failed">{error}</p>}
        <ol className="space-y-1">
          {[...feed].reverse().map((event) => (
            <FeedEntry key={event.id} event={event} />
          ))}
        </ol>
        {feed.length === 0 && !loading && (
          <p className="text-sm text-muted-foreground">Nothing here yet.</p>
        )}
        {truncated && (
          <p className="mt-2 text-xs text-muted-foreground">
            Stopped after {pageBudget} entries, so part of this stretch of
            history is not shown.
          </p>
        )}
        {older && (
          <Button
            variant="ghost"
            size="sm"
            className="mt-2"
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
