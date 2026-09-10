import { History } from 'lucide-react'
import { useEffect } from 'react'
import { FeedEntry } from '@/components/feed-entry'
import { Button } from '@/components/ui/button'
import { Tooltip } from '@/components/ui/heroui'
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
    <Tooltip>
      <Tooltip.Trigger<'button'>
        render={(triggerProps) => (
          <button
            {...triggerProps}
            type="button"
            onClick={() => {
              navigate('timeline')
            }}
            className={cn(
              focusRing,
              'flex h-[22px] min-h-[22px] shrink-0 items-center gap-1 px-1.5 text-xs hover:bg-toolbar-hover hover:text-foreground',
            )}
          >
            <History className="size-3.5" aria-hidden />
            Activity
          </button>
        )}
      />
      <Tooltip.Content>Open Activity</Tooltip.Content>
    </Tooltip>
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
    <div className="flex h-full min-h-0 flex-col">
      <ViewHeader
        title="Activity"
        subtitle={`${feed.length} ${feed.length === 1 ? 'entry' : 'entries'}`}
      />
      <div className="grid shrink-0 grid-cols-1 gap-x-3 gap-y-2 border-b bg-sidebar px-3 py-2 sm:grid-cols-2 sm:px-4 xl:grid-cols-4">
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

      <div className="min-h-0 flex-1 overflow-y-auto p-3 sm:p-4">
        <div className="mx-auto w-full max-w-5xl">
          {error && (
            <p
              role="alert"
              className="mb-3 border-l-2 border-state-failed bg-state-failed/10 px-3 py-2 text-[13px] text-state-failed"
            >
              {error}
            </p>
          )}
          {loading && feed.length === 0 && !error && (
            <div aria-label="Loading activity" className="divide-y border-y border-border">
              <div className="h-12 animate-pulse bg-muted/35" />
              <div className="h-12 animate-pulse bg-muted/20" />
            </div>
          )}
          <ol className="border-y border-border">
            {[...feed].reverse().map((event) => (
              <FeedEntry key={event.id} event={event} runLink />
            ))}
          </ol>
          {feed.length === 0 && !loading && !error && (
            <div className="border-y border-dashed px-4 py-8 text-center">
              <p className="text-sm font-medium">Nothing here yet.</p>
              <p className="mt-1 text-[13px] text-muted-foreground">
                Activity appears here as your team works in this workspace.
              </p>
            </div>
          )}
          {truncated && (
            <p className="mt-3 border-l-2 border-border bg-muted/20 px-3 py-2 text-[13px] text-muted-foreground">
              Stopped after {pageBudget} entries, so part of this stretch of
              history is not shown. Narrow the filters to see it.
            </p>
          )}
          {older && (
            <Button
              variant="outline"
              size="default"
              className="mt-3"
              disabled={loading}
              onClick={() => void olderFeed(useStore, client)}
            >
              Load older
            </Button>
          )}
        </div>
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
    <div className="flex min-w-0 flex-col items-stretch gap-1 text-[13px] font-medium text-foreground">
      <Label htmlFor={control} className="text-muted-foreground">
        {label}
      </Label>
      <Select
        value={value || everything}
        onValueChange={(next) => onChange(next === everything ? '' : next)}
      >
        <SelectTrigger
          id={control}
          className="h-[26px] min-h-[26px] w-full min-w-0 truncate text-[13px] text-foreground"
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
