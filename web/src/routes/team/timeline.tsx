import { History } from 'lucide-react'
import { useEffect } from 'react'
import { FeedEntry } from '@/components/feed-entry'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { eventLabel, type EventType } from '@/lib/events'
import { runLabel } from '@/lib/status'
import { cn, focusRing } from '@/lib/utils'
import type { RouteProps } from '@/routes/registry'
import { drain, olderFeed, openFeed, pageBudget } from '@/routes/team/sync'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

/** The event types worth offering as a filter; empty means everything. */
const filterTypes: EventType[] = [
  'run.status',
  'run.title',
  'run.agent',
  'run.diff',
  'workspace.timeline',
  'workspace.approval',
  'workspace.presence',
  'workspace.budget',
  'run.cost',
  'run.overlap',
  'git.branch',
  'run.protected',
  'sync.conflict',
  'server.update',
]

const types: [string, string][] = [
  ['', 'Everything'],
  ...filterTypes.map((type): [string, string] => [type, eventLabel[type]]),
]

/** The way into the feed, from the status bar. */
export function TimelineStatus() {
  const navigate = useStore((s) => s.navigate)
  if (!useCapability().hasMethod('workspace.timeline')) return null
  return (
    <button
      type="button"
      onClick={() => navigate('timeline')}
      title="Open Activity"
      className={cn(focusRing, 'flex items-center gap-1 rounded px-1 hover:text-foreground')}
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
        title="Activity"
        subtitle={`${feed.length} ${feed.length === 1 ? 'entry' : 'entries'}`}
      />
      <div className="flex flex-wrap items-center gap-2 border-b px-4 py-2 text-xs">
        <FilterSelect
          label="Workspace"
          value={filters.workspaceID}
          // A run belongs to one workspace, so a kept run filter would query
          // the new workspace for a run it does not have and render nothing.
          onChange={(workspaceID) => setFilters({ workspaceID, runID: '' })}
          options={Object.values(workspaces).map((w) => [w.id, w.name])}
        />
        <FilterSelect
          label="Run"
          value={filters.runID}
          onChange={(runID) => setFilters({ runID })}
          options={[['', 'Every run'], ...workspaceRuns.map((r) => [r.id, runLabel(r)])]}
        />
        <FilterSelect
          label="Member"
          value={filters.memberID}
          onChange={(memberID) => setFilters({ memberID })}
          options={[
            ['', 'Everyone'],
            ...Object.values(members).map((m) => [m.id, m.display_name]),
          ]}
        />
        <FilterSelect
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

/** What every filter's "no filter" row travels as. */
const everything = 'all'

function FilterSelect({
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
  const control = `filter-${label.toLowerCase()}`
  return (
    <div className="flex items-center gap-2 text-xs text-muted-foreground">
      <Label htmlFor={control} className="text-xs">
        {label}
      </Label>
      <Select
        value={value || everything}
        onValueChange={(next) => onChange(next === everything ? '' : next)}
      >
        {/* The filter bar sizes its controls to their own text, and sets the
            scale for the row; the shared field style would stretch each one
            and grow it to `text-sm`. */}
        <SelectTrigger
          id={control}
          className="w-auto max-w-44 truncate text-xs text-foreground"
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {options.map(([id, name]) => (
            <SelectItem key={id} value={id || everything}>
              {name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}
