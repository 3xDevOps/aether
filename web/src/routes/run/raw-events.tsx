import { useEffect } from 'react'
import { FeedEntry } from '@/components/feed-entry'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api, type Api } from '@/lib/api'
import { olderFeed, openFeed, pageBudget, useLiveFeed } from '@/routes/team/sync'
import { useStore } from '@/store'
import type { RunRecord } from '@/store/runs'

function RawEvents({ run, client }: { run: RunRecord; client: Api }) {
  const filters = useStore((s) => s.feedFilters)
  const setFilters = useStore((s) => s.setFeedFilters)
  const feed = useStore((s) => s.feed)
  const older = useStore((s) => s.feedOlder)
  const loading = useStore((s) => s.feedLoading)
  const error = useStore((s) => s.feedError)
  const truncated = useStore((s) => s.feedTruncated)
  const pinned = filters.workspaceID === run.workspace_id && filters.runID === run.id

  // Runs before the pin below, so it restores the filters Activity chose.
  useEffect(() => {
    const previous = useStore.getState().feedFilters
    return () => useStore.getState().setFeedFilters(previous)
  }, [])

  useEffect(() => {
    if (!pinned) setFilters({ workspaceID: run.workspace_id, runID: run.id, memberID: '', type: '' })
  }, [run.workspace_id, run.id, pinned, setFilters])

  useEffect(() => {
    if (pinned) void openFeed(useStore, client)
  }, [pinned, filters, client])

  useLiveFeed(pinned, client)

  return (
    <div className="flex min-h-0 flex-col gap-2 overflow-y-auto">
      {error && <Callout tone="failed">{error}</Callout>}
      <ol>
        {[...feed].reverse().map((event) => <FeedEntry key={event.id} event={event} />)}
      </ol>
      {feed.length === 0 && !loading && <p className="text-ui text-muted">Nothing recorded for this run yet.</p>}
      {truncated && <p className="text-ui-sm text-muted">Stopped after {pageBudget} entries, so part of this stretch is not shown.</p>}
      {older && (
        <div>
          <Button variant="ghost" size="sm" disabled={loading} onClick={() => void olderFeed(useStore, client)}>Load older</Button>
        </div>
      )}
    </div>
  )
}

export function RawEventsDialog({ run, open, onOpenChange, returnTo, client = api }: {
  run: RunRecord
  open: boolean
  onOpenChange: (open: boolean) => void
  returnTo: HTMLElement | null
  client?: Api
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="flex max-w-2xl flex-col"
        onCloseAutoFocus={(event) => {
          if (!returnTo?.isConnected) return
          event.preventDefault()
          returnTo.focus()
        }}
      >
        <DialogHeader>
          <DialogTitle>Raw events</DialogTitle>
          <DialogDescription>Everything the server recorded about this run, newest first.</DialogDescription>
        </DialogHeader>
        {open && <RawEvents run={run} client={client} />}
      </DialogContent>
    </Dialog>
  )
}
