import { History } from 'lucide-react'
import { useEffect } from 'react'
import { FeedEntry } from '@/components/feed-entry'
import { Button } from '@/components/ui/button'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { runLabel } from '@/lib/status'
import type { RouteProps } from '@/routes/registry'
import { drain, olderFeed, openFeed, pageBudget } from '@/routes/team/sync'
import { useStore } from '@/store'

/** The event types worth offering as a filter; empty means everything. */
const types = [
  ['', 'Everything'],
  ['run.status', 'Run status'],
  ['run.title', 'Run titles'],
  ['workspace.timeline', 'Steering'],
  ['workspace.approval', 'Approvals'],
  ['workspace.presence', 'Presence'],
  ['run.cost', 'Cost'],
  ['run.overlap', 'Conflicts'],
  ['git.branch', 'Branches'],
  ['server.update', 'Server updates'],
]

/** The way into the feed, from the status bar. */
export function TimelineStatus() {
  const navigate = useStore((s) => s.navigate)
  return (
    <button
      type="button"
      onClick={() => navigate('timeline')}
      title="Workspace activity"
      className="flex items-center gap-1 rounded px-1 hover:text-foreground"
    >
      <History className="size-3.5" aria-hidden />
      Activity
    </button>
  )
}

/**
 * One workspace's history, newest first, filterable by run, member and type.
 * The reader pages forward from a cursor, so the view opens on a window at
 * the end of the log and "load older" walks that window back.
 *
 * This keeps a workspace picker where the other scoped surfaces dropped
 * theirs: comparing what happened in one workspace against another is the
 * whole point of an activity log, so the sidebar's choice is only the
 * default here.
 */
export function TimelineFeed({ params, client = api }: RouteProps & { client?: Api }) {
  const workspaces = useStore((s) => s.workspaces)
  const activeWorkspace = useStore((s) => s.activeWorkspace)
  const runs = useStore((s) => s.runs)
  const members = useStore((s) => s.members)
  const filters = useStore((s) => s.feedFilters)
  const setFilters = useStore((s) => s.setFeedFilters)
  const feed = useStore((s) => s.feed)
  const older = useStore((s) => s.feedOlder)
  const loading = useStore((s) => s.feedLoading)
  const error = useStore((s) => s.feedError)
  const truncated = useStore((s) => s.feedTruncated)
  const lastSeq = useStore((s) => s.lastSeq)

  // The feed is workspace-scoped: prefer the workspace the caller named, then
  // the active one, then any we know.
  useEffect(() => {
    if (filters.workspaceID) return
    const first =
      params.workspaceId || activeWorkspace || Object.keys(workspaces)[0]
    if (first) setFilters({ workspaceID: first })
  }, [
    filters.workspaceID,
    params.workspaceId,
    activeWorkspace,
    workspaces,
    setFilters,
  ])

  useEffect(() => {
    void openFeed(useStore, client)
  }, [filters, client])

  // Live tail: every applied event moves the cursor, and whatever landed
  // after ours is one page away.
  useEffect(() => {
    if (!useStore.getState().feedLoading) void drain(useStore, client)
  }, [lastSeq, client])

  const workspaceRuns = Object.values(runs).filter(
    (r) => r.workspace_id === filters.workspaceID,
  )

  return (
    <div className="flex h-full flex-col">
      <ViewHeader
        title="Workspace activity"
        subtitle={`${feed.length} ${feed.length === 1 ? 'entry' : 'entries'}`}
      />
      <div className="flex flex-wrap items-center gap-2 border-b px-4 py-2 text-xs">
        <Select
          label="Workspace"
          value={filters.workspaceID}
          // A run belongs to one workspace, so a kept run filter would query
          // the new workspace for a run it does not have and render nothing.
          onChange={(workspaceID) => setFilters({ workspaceID, runID: '' })}
          options={Object.values(workspaces).map((w) => [w.id, w.name])}
        />
        <Select
          label="Run"
          value={filters.runID}
          onChange={(runID) => setFilters({ runID })}
          options={[['', 'Every run'], ...workspaceRuns.map((r) => [r.id, runLabel(r)])]}
        />
        <Select
          label="Member"
          value={filters.memberID}
          onChange={(memberID) => setFilters({ memberID })}
          options={[
            ['', 'Everyone'],
            ...Object.values(members).map((m) => [m.id, m.display_name]),
          ]}
        />
        <Select
          label="Type"
          value={filters.type}
          onChange={(type) => setFilters({ type })}
          options={types}
        />
      </div>

      <div className="flex-1 overflow-y-auto p-3">
        {error && <p className="mb-2 text-xs text-state-failed">{error}</p>}
        <ol className="space-y-1">
          {[...feed].reverse().map((event) => (
            <FeedEntry key={event.id} event={event} runLink />
          ))}
        </ol>
        {feed.length === 0 && !loading && (
          <p className="text-sm text-muted-foreground">Nothing here yet.</p>
        )}
        {truncated && (
          <p className="mt-2 text-xs text-muted-foreground">
            Stopped after {pageBudget} entries, so part of this stretch of
            history is not shown. Narrow the filters to see it.
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

function Select({
  label,
  value,
  onChange,
  options,
}: {
  label: string
  value: string
  onChange: (value: string) => void
  options: (string[] | [string, string])[]
}) {
  return (
    <label className="flex items-center gap-2 text-muted-foreground">
      {label}
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="max-w-44 truncate rounded-md border bg-background px-2 py-1 text-foreground"
      >
        {options.map(([id, name]) => (
          <option key={id} value={id}>
            {name}
          </option>
        ))}
      </select>
    </label>
  )
}
