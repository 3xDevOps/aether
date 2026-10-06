import { RefreshCw, WrapText } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { MissingRun } from '@/components/missing-run'
import { RunHeader } from '@/components/run-header'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Badge } from '@/components/ui/badge'
import { Tooltip } from '@/components/ui/tooltip'
import { RelativeTime } from '@/components/ui/relative-time'
import { api } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { cn, focusRing } from '@/lib/utils'
import { ConflictChips } from '@/routes/diff/conflict-chips'
import { Land } from '@/routes/diff/land'
import { parsePatch } from '@/routes/diff/parse'
import { NativeChanges } from '@/routes/diff/native-changes'
import { FilePatch } from '@/routes/diff/patch-view'
import { ReviewCommands } from '@/routes/diff/review-commands'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { runTabPanel } from '@/routes/terminal/tabs'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import {
  initialDiff,
  intervalKey,
  type DiffSnapshot,
  type IntervalPatch,
  type RunDiffState,
} from '@/store/diff'

/** A snapshot shows the diff between its parent tree and its tree, not a filter
 * over the current diff; a snapshot with no recorded tree is not selectable. */
function DiffView({ params }: RouteProps) {
  const caps = useCapability()
  const runID = params.runId
  const run = useStore((s) => s.runs[runID])
  const state = useStore((s) => s.diffs[runID] ?? initialDiff)
  // Keyed on the snapshot's time, not its index: new snapshots are prepended,
  // so an index would silently retarget whenever one arrived.
  const [selected, setSelected] = useState<string | null>(null)
  // Null follows the pointer default. Kept on the UI slice: component state
  // would be lost on every trip to the Terminal tab.
  const wrapping = useStore((s) => s.diffWrap)
  const setWrapping = useStore((s) => s.setDiffWrap)
  const coarse = useMediaQuery(coarsePointer)
  const stacked = useIsMobile()
  const wrap = wrapping ?? coarse
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
  const hidden = stacked && state.snapshots.length === 0

  if (!run) {
    return <MissingRun />
  }

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col">
      <RunHeader run={run} subtitle={run.branch} active="diff" />
      <div {...runTabPanel('diff', 'flex min-h-0 min-w-0 flex-1 flex-col overflow-y-auto md:overflow-hidden')}>
        <Land run={run} />
        {caps.hasMethod('run.git.status') && <NativeChanges key={runID} run={run} wrap={wrap} />}

        <div className="shrink-0 bg-sidebar">
          <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 border-b px-3 py-2 text-[12px] text-muted-foreground">
            <div className="min-w-0 flex-[1_1_16rem]">
              <p className="truncate font-medium text-foreground">
                {snapshot ? 'Interval review' : 'Current diff'}
              </p>
              <p className="truncate">
                {snapshot ? (
                  <>What changed <RelativeTime at={snapshot.time} /></>
                ) : (
                  <>
                    Against{' '}
                    <code title={state.base} className="font-mono">
                      {state.base.slice(0, 8) || 'the fork point'}
                    </code>
                  </>
                )}
              </p>
            </div>
            <div className="flex min-w-0 max-w-full flex-wrap items-center gap-1.5">
              <Badge>
                <span className="font-mono">
                  {shown.length} file{shown.length === 1 ? '' : 's'}
                </span>
              </Badge>
              <Badge tone="done">
                <span className="font-mono">+{total(shown, 'additions')}</span>
              </Badge>
              <Badge tone="failed">
                <span className="font-mono">-{total(shown, 'deletions')}</span>
              </Badge>
              <ConflictChips run={run} />
              {snapshot && (
                <Button
                  variant="secondary"
                  size="sm"
                  aria-label="Show current diff"
                  onClick={() => setSelected(null)}
                >
                  Current diff
                </Button>
              )}
              <Button
                variant={wrap ? 'secondary' : 'ghost'}
                size="sm"
                className="px-2"
                aria-pressed={wrap}
                onClick={() => setWrapping(!wrap)}
              >
                <WrapText className="size-3.5" aria-hidden />
                Wrap lines
              </Button>
              <Button
                variant="ghost"
                size="sm"
                className="px-2"
                onClick={() => useStore.getState().refreshDiff(runID)}
              >
                <RefreshCw
                  className={cn('size-3.5', state.status === 'loading' && 'animate-spin')}
                  aria-hidden
                />
                Refresh
              </Button>
            </div>
          </div>
          <ReviewCommands run={run} />
        </div>

        {failed && (
          <p role="alert" className="shrink-0 border-b bg-destructive/10 px-3 py-1.5 text-[12px] text-destructive">
            {error ?? 'The diff could not be loaded.'}
          </p>
        )}
        {truncated && (
          <p className="shrink-0 border-b bg-state-waiting/10 px-3 py-1.5 text-[12px] text-muted-foreground">
            The server returned an incomplete diff. Refresh it or fetch the run
            branch to read the complete change.
          </p>
        )}
        <div
          className={cn(
            'grid min-h-0 min-w-0 flex-none grid-cols-1 md:flex-1 md:overflow-hidden',
            !hidden && 'md:grid-cols-[14rem_minmax(0,1fr)]',
          )}
        >
          {!hidden && (
            <Timeline
              snapshots={state.snapshots}
              selected={selected}
              onSelect={(time) => setSelected(time === selected ? null : time)}
              stacked={stacked}
            />
          )}
          <div className="min-h-0 min-w-0 overflow-y-auto overflow-x-hidden bg-background">
            <div>
              {shown.map((file) => (
                <FilePatch key={file.path} file={file} wrap={wrap} />
              ))}
              {shown.length === 0 && note && (
                <p className="border-b border-dashed p-4 text-[12px] text-muted-foreground">
                  {note}
                </p>
              )}
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

/** A failed fetch returns null: the banner above already shows the server's message. */
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

const noTree =
  'This server did not record a tree for this snapshot, so what changed ' +
  'then cannot be shown.'

function range(snapshot: DiffSnapshot): { from: string; to: string } | null {
  if (!snapshot.tree || !snapshot.parentTree) return null
  return { from: snapshot.parentTree, to: snapshot.tree }
}

function Timeline({
  snapshots,
  selected,
  onSelect,
  stacked,
}: {
  snapshots: DiffSnapshot[]
  selected: string | null
  onSelect: (time: string) => void
  stacked: boolean
}) {
  const rows = (
    <ul className={cn('p-1', stacked && 'max-h-52 overflow-y-auto')}>
      {snapshots.map((snap, i) => {
        const shownable = range(snap) !== null
        return (
          <li key={snap.time + i}>
            {/* Disabled, not empty: an empty open tooltip still sets a dangling
                `aria-describedby` and swallows the first Escape. */}
            <Tooltip disabled={shownable} content={noTree}>
              <button
                type="button"
                aria-disabled={!shownable || undefined}
                onClick={() => {
                  if (shownable) onSelect(snap.time)
                }}
                aria-pressed={selected === snap.time}
                className={cn(
                  focusRing,
                  'min-h-10 w-full border-l-2 border-transparent px-2 py-1.5 text-left text-[12px] hover:not-aria-disabled:bg-toolbar-hover',
                  'aria-disabled:cursor-not-allowed aria-disabled:opacity-50',
                  selected === snap.time && 'border-primary bg-selection text-selection-foreground',
                )}
              >
                <RelativeTime at={snap.time} className="block truncate font-medium" />
                <span
                  className={cn(
                    'mt-0.5 block text-[11px] text-muted-foreground',
                    selected === snap.time && 'text-selection-foreground/80',
                  )}
                >
                  {snap.files.length} file{snap.files.length === 1 ? '' : 's'}
                  {' · '}
                  <span className="font-mono text-success-foreground">
                    +{total(snap.files, 'additions')}
                  </span>{' '}
                  <span className="font-mono text-destructive">
                    -{total(snap.files, 'deletions')}
                  </span>
                </span>
              </button>
            </Tooltip>
          </li>
        )
      })}
    </ul>
  )

  if (stacked) {
    return (
      <Collapsible asChild>
        <aside className="min-h-0 border-b bg-sidebar">
          <CollapsibleTrigger className="min-h-[35px] px-3 py-2 text-[12px] font-medium text-foreground">
            Change intervals ({snapshots.length})
          </CollapsibleTrigger>
          <CollapsibleContent>{rows}</CollapsibleContent>
        </aside>
      </Collapsible>
    )
  }

  return (
    <aside className="min-h-0 overflow-y-auto border-r bg-sidebar">
      <div className="sticky top-0 z-10 min-h-[35px] border-b bg-sidebar px-3 py-2">
        <h2 className="text-[12px] font-medium text-foreground">Change intervals</h2>
        <p className="mt-0.5 text-[11px] text-muted-foreground">
          {snapshots.length === 0
            ? 'Nothing since you opened the dashboard.'
            : 'Select an interval to review what changed.'}
        </p>
      </div>
      {rows}
    </aside>
  )
}

function total<K extends string>(items: Record<K, number>[], key: K): number {
  return items.reduce((sum, item) => sum + item[key], 0)
}

/** The store holds the patch, so returning to the tab reuses what was fetched. */
function usePatch(runID: string): void {
  const revision = useStore((s) => s.diffs[runID]?.revision ?? 0)
  const fetched = useStore((s) => s.diffs[runID]?.fetched ?? -1)
  // A snapshot landing mid-request must neither cancel it nor start a second one:
  // the response records its revision and the next render asks again if needed.
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
        // Recorded as answered so a failure cannot spin.
        useStore.getState().setDiff(runID, {
          status: 'error',
          fetched: at,
          error: err instanceof Error ? err.message : String(err),
        })
      })
  }, [runID, revision, fetched])
}

/** The interval response's `base` is the `from` tree, so it must not be written
 * into the run's `base`, which names the fork point. */
function useInterval(
  runID: string,
  snapshot: DiffSnapshot | null,
): IntervalPatch | undefined {
  const at = snapshot ? range(snapshot) : null
  const from = at?.from ?? ''
  const to = at?.to ?? ''
  const key = at ? intervalKey(from, to) : ''
  const entry = useStore((s) => (key ? s.diffs[runID]?.intervals[key] : undefined))
  // Not the entry itself: only its presence decides whether to ask, and
  // depending on the object would re-run the effect on every write to it.
  const cached = entry !== undefined

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
  }, [runID, key, from, to, cached])

  return entry
}

registerRoute('diff', DiffView)
