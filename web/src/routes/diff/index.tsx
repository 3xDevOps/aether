import { RefreshCw } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { RunActions } from '@/components/run-actions'
import { Button } from '@/components/ui/button'
import { ViewHeader } from '@/components/view-header'
import { api } from '@/lib/api'
import { timeAgo } from '@/lib/format'
import { runLabel } from '@/lib/status'
import { cn } from '@/lib/utils'
import { ConflictChips } from '@/routes/diff/conflict-chips'
import { Land } from '@/routes/diff/land'
import { parsePatch } from '@/routes/diff/parse'
import { FilePatch } from '@/routes/diff/patch-view'
import { ReviewCommands } from '@/routes/diff/review-commands'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { RunTabs } from '@/routes/terminal/tabs'
import { useStore } from '@/store'
import {
  initialDiff,
  intervalKey,
  type DiffSnapshot,
  type IntervalPatch,
  type RunDiffState,
} from '@/store/diff'

/**
 * The run-detail Diff tab: the run's current diff against its fork point,
 * plus the times its files changed. The server records a git tree per
 * snapshot, so selecting one shows the diff between the tree before it and
 * the tree at it - what that interval alone changed, not a filter over the
 * current diff. A snapshot from a server that recorded no tree cannot be
 * shown that way, and is not selectable.
 */
function DiffView({ params }: RouteProps) {
  const runID = params.runId
  const run = useStore((s) => s.runs[runID])
  const state = useStore((s) => s.diffs[runID] ?? initialDiff)
  // Keyed on the snapshot's time, not its index: new snapshots are prepended,
  // so an index would silently retarget whenever one arrived.
  const [selected, setSelected] = useState<string | null>(null)
  usePatch(run ? runID : '')

  const snapshot =
    selected === null
      ? null
      : (state.snapshots.find((s) => s.time === selected) ?? null)
  const interval = useInterval(run ? runID : '', snapshot)
  const cumulative = useMemo(() => parsePatch(state.patch), [state.patch])
  const changed = useMemo(() => parsePatch(interval?.patch ?? ''), [interval?.patch])

  const shown = snapshot ? changed : cumulative
  const error = snapshot ? interval?.error : state.error
  const failed = snapshot ? interval?.status === 'error' : state.status === 'error'
  const truncated = snapshot ? (interval?.truncated ?? false) : state.truncated
  const note = emptyNote(snapshot, interval, state)

  if (!run) {
    return <p className="p-4 text-sm text-muted-foreground">Unknown run.</p>
  }

  return (
    <div className="flex h-full flex-col">
      <ViewHeader
        title={runLabel(run)}
        subtitle={run.branch}
        actions={<RunActions run={run} />}
      />
      <RunTabs runID={runID} active="diff" />
      <div className="px-4 pt-3">
        <Land run={run} />
      </div>

      <div className="flex flex-wrap items-center gap-2 border-b px-4 py-1.5 text-xs text-muted-foreground">
        {snapshot ? (
          <span>What changed {timeAgo(snapshot.time)}</span>
        ) : (
          <span>
            Current diff against{' '}
            <code title={state.base}>{state.base.slice(0, 8) || 'the fork point'}</code>
          </span>
        )}
        <span>
          {shown.length} file{shown.length === 1 ? '' : 's'}
        </span>
        <span className="text-state-done">+{total(shown, 'additions')}</span>
        <span className="text-destructive">-{total(shown, 'deletions')}</span>
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
        <ReviewCommands run={run} />
      </div>

      {failed && (
        <p className="border-b bg-destructive/10 px-4 py-1.5 text-xs">
          {error ?? 'The diff could not be loaded.'}
        </p>
      )}
      {truncated && (
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
          {shown.length === 0 && note && (
            <p className="text-sm text-muted-foreground">{note}</p>
          )}
        </div>
      </div>
    </div>
  )
}

/** What to say when there is nothing to render. A failed fetch says nothing
 * here: the banner above already carries the server's own message. */
function emptyNote(
  snapshot: DiffSnapshot | null,
  interval: IntervalPatch | undefined,
  state: RunDiffState,
): string | null {
  if (snapshot) {
    if (!interval || interval.status === 'loading') return 'Loading what changed then...'
    if (interval.status === 'error') return null
    return 'That interval recorded no textual change.'
  }
  if (state.status === 'loading') return 'Loading the diff...'
  if (state.status === 'error') return null
  return 'Nothing has changed against the fork point yet.'
}

/** Why a snapshot cannot be opened. Shown on the row itself, since there is
 * nothing to select. */
const noTree =
  'This server did not record a tree for this snapshot, so what changed ' +
  'then cannot be shown.'

/** The trees bounding the interval a snapshot ended, or null when the server
 * that sent it recorded none. */
function range(snapshot: DiffSnapshot): { from: string; to: string } | null {
  if (!snapshot.tree || !snapshot.parentTree) return null
  return { from: snapshot.parentTree, to: snapshot.tree }
}

/** When files changed: one entry per diff snapshot the server took. Selecting
 * one shows the diff of that interval. */
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
          : 'Selecting one shows what that interval changed.'}
      </p>
      <ul>
        {snapshots.map((snap, i) => {
          const shownable = range(snap) !== null
          return (
            <li key={snap.time + i}>
              <button
                type="button"
                disabled={!shownable}
                title={shownable ? undefined : noTree}
                onClick={() => onSelect(snap.time)}
                aria-pressed={selected === snap.time}
                className={cn(
                  'w-full px-3 py-1.5 text-left text-xs hover:bg-accent/50',
                  selected === snap.time && 'bg-accent',
                  !shownable && 'cursor-not-allowed opacity-60 hover:bg-transparent',
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
          )
        })}
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

/**
 * The selected snapshot's interval patch, fetched the first time it is asked
 * for. The two trees name the answer, so a cached entry - a failure included
 * - is never refetched; the run's cumulative patch is what Refresh reloads.
 * The interval response's `base` is the `from` tree, so it is deliberately
 * not written into the run's `base`, which names the fork point.
 */
function useInterval(
  runID: string,
  snapshot: DiffSnapshot | null,
): IntervalPatch | undefined {
  const at = snapshot ? range(snapshot) : null
  const from = at?.from ?? ''
  const to = at?.to ?? ''
  const key = at ? intervalKey(from, to) : ''
  const entry = useStore((s) => (key ? s.diffs[runID]?.intervals[key] : undefined))

  useEffect(() => {
    if (!runID || !key) return
    const store = useStore.getState()
    // Read through the store rather than the rendered entry: the write below
    // is what stops a second fetch, and only the store has it immediately.
    if (store.diffs[runID]?.intervals[key]) return
    store.setIntervalPatch(runID, key, { patch: '', truncated: false, status: 'loading' })
    api
      .runPatch(runID, { from, to })
      .then((patch) => {
        useStore.getState().setIntervalPatch(runID, key, {
          patch: patch.patch,
          truncated: patch.truncated,
          status: 'ready',
        })
      })
      .catch((err: unknown) => {
        useStore.getState().setIntervalPatch(runID, key, {
          patch: '',
          truncated: false,
          status: 'error',
          error: err instanceof Error ? err.message : String(err),
        })
      })
  }, [runID, key, from, to])

  return entry
}

registerRoute('diff', DiffView)
