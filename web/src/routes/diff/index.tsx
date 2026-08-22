import { RefreshCw } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { ViewHeader } from '@/components/view-header'
import { api } from '@/lib/api'
import { timeAgo } from '@/lib/format'
import { cn } from '@/lib/utils'
import { ConflictChips } from '@/routes/diff/conflict-chips'
import { parsePatch } from '@/routes/diff/parse'
import { FilePatch } from '@/routes/diff/patch-view'
import { LandControls } from '@/routes/diff/land'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { RunTabs } from '@/routes/terminal/tabs'
import { useStore } from '@/store'
import { initialDiff, type DiffSnapshot } from '@/store/diff'

/**
 * The run-detail Diff tab: the run's current diff against its fork point,
 * plus the times its files changed. It is not a per-interval delta - nothing
 * records a tree per snapshot, so selecting a snapshot narrows the current
 * diff to that snapshot's files rather than replaying that interval. The
 * labels say so;  is the server work that would make it one.
 */
function DiffView({ params }: RouteProps) {
  const runID = params.runId
  const run = useStore((s) => s.runs[runID])
  const state = useStore((s) => s.diffs[runID] ?? initialDiff)
  // Keyed on the snapshot's time, not its index: new snapshots are prepended,
  // so an index would silently retarget whenever one arrived.
  const [selected, setSelected] = useState<string | null>(null)
  usePatch(run ? runID : '')

  const files = useMemo(() => parsePatch(state.patch), [state.patch])
  const snapshot =
    selected === null
      ? null
      : (state.snapshots.find((s) => s.time === selected) ?? null)
  const shown = snapshot
    ? files.filter((f) => snapshot.files.some((s) => s.path === f.path))
    : files

  if (!run) {
    return <p className="p-4 text-sm text-muted-foreground">Unknown run.</p>
  }

  return (
    <div className="flex h-full flex-col">
      <ViewHeader title={run.task} subtitle={run.branch} />
      <RunTabs runID={runID} active="diff" />

      <div className="flex flex-wrap items-center gap-2 border-b px-4 py-1.5 text-xs text-muted-foreground">
        <span>
          Current diff against{' '}
          <code title={state.base}>{state.base.slice(0, 8) || 'the fork point'}</code>
        </span>
        <span>
          {shown.length} file{shown.length === 1 ? '' : 's'}
        </span>
        <span className="text-state-done">+{total(shown, 'additions')}</span>
        <span className="text-destructive">-{total(shown, 'deletions')}</span>
        {snapshot && <span>narrowed to what changed {timeAgo(snapshot.time)}</span>}
        <ConflictChips run={run} />
        <Button
          variant="ghost"
          size="sm"
          className="ml-auto h-6 px-2"
          onClick={() => useStore.getState().refreshDiff(runID)}
        >
          <RefreshCw className={cn('size-3', state.status === 'loading' && 'animate-spin')} />
          Refresh
        </Button>
        <LandControls run={run} />
      </div>

      {state.status === 'error' && (
        <p className="border-b bg-destructive/10 px-4 py-1.5 text-xs">
          {state.error ?? 'The diff could not be loaded.'}
        </p>
      )}
      {state.truncated && (
        <p className="border-b bg-state-waiting/10 px-4 py-1.5 text-xs">
          This diff is too large to render in full; everything below the cut is
          missing. Fetch the run branch to read it whole.
        </p>
      )}

      <div className="grid min-h-0 flex-1 grid-cols-1 md:grid-cols-[13rem_1fr]">
        <Timeline
          snapshots={state.snapshots}
          selected={selected}
          onSelect={(time) => setSelected(time === selected ? null : time)}
        />
        <div className="min-w-0 space-y-3 overflow-auto p-3">
          {shown.map((file) => (
            <FilePatch key={file.path} file={file} />
          ))}
          {shown.length === 0 && (
            <p className="text-sm text-muted-foreground">
              {state.status === 'loading'
                ? 'Loading the diff...'
                : snapshot
                  ? 'None of those files are in the current diff any more: changed again since, or reverted.'
                  : 'Nothing has changed against the fork point yet.'}
            </p>
          )}
        </div>
      </div>
    </div>
  )
}

/** When files changed: one entry per diff snapshot the server took. Each is a
 * filter over the current diff, not the diff of that interval. */
function Timeline({
  snapshots,
  selected,
  onSelect,
}: {
  snapshots: DiffSnapshot[]
  selected: string | null
  onSelect: (time: string) => void
}) {
  return (
    <aside className="overflow-auto border-b md:border-r md:border-b-0">
      <h2 className="px-3 pt-2 text-xs font-medium text-muted-foreground">
        When files changed
      </h2>
      <p className="px-3 py-1 text-xs text-muted-foreground">
        {snapshots.length === 0
          ? 'Nothing since you opened the dashboard.'
          : 'Selecting one narrows the diff to the files that changed then.'}
      </p>
      <ul>
        {snapshots.map((snap, i) => (
          <li key={snap.time + i}>
            <button
              type="button"
              onClick={() => onSelect(snap.time)}
              aria-pressed={selected === snap.time}
              className={cn(
                'w-full px-3 py-1.5 text-left text-xs hover:bg-accent/50',
                selected === snap.time && 'bg-accent',
              )}
            >
              <span className="block">{timeAgo(snap.time)}</span>
              <span className="block text-muted-foreground">
                {snap.files.length} file{snap.files.length === 1 ? '' : 's'}
                {' · '}
                <span className="text-state-done">+{total(snap.files, 'additions')}</span>{' '}
                <span className="text-destructive">-{total(snap.files, 'deletions')}</span>
              </span>
            </button>
          </li>
        ))}
      </ul>
    </aside>
  )
}

function total<K extends string>(items: Record<K, number>[], key: K): number {
  return items.reduce((sum, item) => sum + item[key], 0)
}

/**
 * Fetches the patch whenever the run's revision has moved past the one the
 * stored patch answers for. The store, not this component, holds the answer,
 * so leaving the tab and coming back re-renders what was already fetched.
 */
function usePatch(runID: string): void {
  const revision = useStore((s) => s.diffs[runID]?.revision ?? 0)
  const fetched = useStore((s) => s.diffs[runID]?.fetched ?? -1)
  // The run whose patch is in flight. A snapshot landing mid-request must
  // neither cancel it nor start a second one: the response records the
  // revision it answered for, and the re-render that follows asks again if a
  // newer one arrived meanwhile.
  const inFlight = useRef<string | null>(null)

  useEffect(() => {
    if (!runID || revision === fetched || inFlight.current === runID) return
    const at = revision
    inFlight.current = runID
    const done = () => {
      if (inFlight.current === runID) inFlight.current = null
    }
    useStore.getState().setDiff(runID, { status: 'loading' })
    api
      .runPatch(runID)
      .then((patch) => {
        done()
        useStore.getState().applyPatch(patch, at)
      })
      .catch((err: unknown) => {
        done()
        // Recorded as answered so a failure cannot spin. The next snapshot
        // and the Refresh button both ask again.
        useStore.getState().setDiff(runID, {
          status: 'error',
          fetched: at,
          error: err instanceof Error ? err.message : String(err),
        })
      })
  }, [runID, revision, fetched])
}

registerRoute('diff', DiffView)
