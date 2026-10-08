import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import { Virtualizer, type VirtualizerHandle } from 'virtua'
import { MissingRun } from '@/components/missing-run'
import { Callout } from '@/components/ui/callout'
import { EmptyState } from '@/components/ui/empty-state'
import { api } from '@/lib/api'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { FileList } from '@/routes/diff/file-list'
import { Land } from '@/routes/diff/land'
import { parsePatch, type PatchFile } from '@/routes/diff/parse'
import { contentLines, FilePatch, largeFile } from '@/routes/diff/patch-view'
import { hasTree, SummaryStrip } from '@/routes/diff/strip'
import { isLiveRun } from '@/routes/files/sources'
import { openInFiles } from '@/routes/files/open'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import { initialDiff, intervalKey, type DiffSnapshot, type IntervalPatch } from '@/store/diff'

const largePatch = 1500

function collapsedByDefault(file: PatchFile): boolean {
  return contentLines(file) > largeFile || file.status === 'binary' || file.status === 'deleted'
}

/** A snapshot shows the diff between its parent tree and its tree, not a filter
 * over the current diff; a snapshot with no recorded tree is not selectable. */
export function ChangesView({ runID }: { runID: string }) {
  const caps = useCapability()
  const run = useStore((s) => s.runs[runID])
  const state = useStore((s) => s.diffs[runID] ?? initialDiff)
  // Keep the chosen interval even when newer snapshots move it out of the menu.
  const [snapshot, setSnapshot] = useState<DiffSnapshot | null>(null)
  const wrapping = useStore((s) => s.diffWrap)
  const setWrapping = useStore((s) => s.setDiffWrap)
  const coarse = useMediaQuery(coarsePointer)
  const wrap = wrapping ?? coarse
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})
  const [current, setCurrent] = useState<string | null>(null)
  const ids = useId()
  const [focusedFile, setFocusedFile] = useState<number | null>(null)
  const list = useRef<VirtualizerHandle>(null)
  usePatch(run ? runID : '')

  const outsideWindow = snapshot !== null && !state.snapshots.some((entry) => entry.time === snapshot.time)
  const interval = useInterval(run ? runID : '', outsideWindow ? null : snapshot)
  const cumulative = useMemo(() => parsePatch(state.patch), [state.patch])
  const changed = useMemo(() => parsePatch(interval?.patch ?? ''), [interval?.patch])
  const files = snapshot ? changed : cumulative
  const virtual = useMemo(() => files.reduce((total, file) => total + contentLines(file), 0) > largePatch, [files])
  const error = outsideWindow ? 'This interval is no longer in the dashboard history window.' : snapshot ? interval?.error : state.error
  const failed = snapshot ? outsideWindow || interval?.status === 'error' : state.status === 'error'
  const loading = snapshot ? !outsideWindow && (!interval || interval.status === 'loading') : state.status === 'loading'
  const truncated = snapshot ? (interval?.truncated ?? false) : state.truncated
  const historyNotice = snapshot ?? state.snapshots.find((entry) => entry.historyGap || entry.snapshotError)

  const toggle = useCallback((path: string, next: boolean) => setCollapsed((all) => ({ ...all, [path]: next })), [])
  const open = useCallback((path: string) => {
    if (run) openInFiles(run, path)
  }, [run])
  const jump = useCallback((path: string) => {
    setCurrent(path)
    setCollapsed((all) => ({ ...all, [path]: false }))
    const index = files.findIndex((file) => file.path === path)
    requestAnimationFrame(() => {
      if (virtual) list.current?.scrollToIndex(index, { align: 'start' })
      else document.getElementById(`${ids}-${index}`)?.scrollIntoView({ block: 'start' })
    })
  }, [files, virtual, ids])

  if (!run) return <MissingRun />

  const canOpen = caps.hasMethod('files.tree') && isLiveRun(run)
  const openable = (file: PatchFile) =>
    file.status !== 'deleted' && file.status !== 'binary' && cumulative.find((entry) => entry.path === file.path)?.status !== 'deleted'
  const patchAt = (file: PatchFile, index: number) => (
    <FilePatch
      key={file.path}
      id={`${ids}-${index}`}
      file={file}
      wrap={wrap}
      lineNumbers
      collapsed={collapsed[file.path] ?? collapsedByDefault(file)}
      onCollapsedChange={toggle}
      onOpen={canOpen && openable(file) ? open : undefined}
    />
  )
  return (
    <div className="@container flex h-full min-h-0 min-w-0 flex-col bg-canvas">
      <Land run={run} />
      <SummaryStrip
        run={run}
        files={files}
        snapshots={state.snapshots}
        selected={snapshot}
        onSelect={(time) => setSnapshot(time === null ? null : state.snapshots.find((entry) => entry.time === time) ?? null)}
        base={state.base}
        wrap={wrap}
        onWrap={setWrapping}
        loading={state.status === 'loading'}
        onRefresh={() => useStore.getState().refreshDiff(runID)}
        onJump={jump}
        onCollapseAll={(next) => setCollapsed(Object.fromEntries(files.map((file) => [file.path, next])))}
        publishable={!state.recorded && cumulative.length > 0}
      />
      {!snapshot && state.recorded && (
        <Callout tone="neutral" role="status" className="m-2 shrink-0">
          Checkout removed. Showing the last recorded changes.
        </Callout>
      )}
      {(historyNotice?.historyGap || historyNotice?.snapshotError) && (
        <Callout tone={historyNotice.snapshotError ? 'failed' : 'needs-you'} role="status" className="m-2 shrink-0">
          {historyNotice.snapshotError && <p>{historyNotice.snapshotError}</p>}
          <p>{snapshot
            ? 'This interval spans a gap in recorded snapshot history; intermediate changes are unavailable.'
            : 'Some snapshot history is unavailable. The current diff is separate and still shows changes against the fork point.'}</p>
        </Callout>
      )}
      {failed && (
        <Callout tone="failed" role="alert" className="m-2 shrink-0">
          {error ?? 'The diff could not be loaded.'}
          {snapshot && <p>This historical interval is unavailable. Choose Current diff to view the cumulative changes.</p>}
        </Callout>
      )}
      {truncated && (
        <Callout tone="needs-you" className="m-2 shrink-0">
          The server returned an incomplete diff. Refresh it or fetch the run branch to read the complete change.
        </Callout>
      )}
      <div className="flex min-h-0 flex-1">
        {files.length > 0 && <FileList files={files} current={current} onJump={jump} />}
        <div className="min-w-0 flex-1 overflow-x-hidden overflow-y-auto">
          {virtual ? (
            <div onFocus={(event) => {
              const id = (event.target as HTMLElement).closest('section')?.id ?? ''
              if (id.startsWith(`${ids}-`)) setFocusedFile(Number(id.slice(ids.length + 1)))
            }}>
              <Virtualizer ref={list} data={files} keepMounted={focusedFile !== null && focusedFile < files.length ? [focusedFile] : undefined}>
                {patchAt}
              </Virtualizer>
            </div>
          ) : (
            files.map(patchAt)
          )}
          {files.length === 0 && !failed && <Empty snapshot={snapshot} loading={loading} recorded={state.recorded} />}
        </div>
      </div>
    </div>
  )
}

function Empty({ snapshot, loading, recorded }: { snapshot: DiffSnapshot | null; loading: boolean; recorded: boolean }) {
  if (loading) {
    return <p className="p-4 text-ui text-muted">{snapshot ? 'Loading what changed then…' : 'Loading the diff…'}</p>
  }
  if (snapshot) return <EmptyState title="No changes in this interval.">That interval recorded no textual change.</EmptyState>
  if (recorded) return <EmptyState title="No recorded changes.">The last snapshot matched the fork point.</EmptyState>
  return <EmptyState title="No changes yet.">Nothing differs from the fork point. Files the agent changes show up here.</EmptyState>
}

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
function useInterval(runID: string, snapshot: DiffSnapshot | null): IntervalPatch | undefined {
  const from = snapshot && hasTree(snapshot) ? snapshot.parentTree! : ''
  const to = snapshot && hasTree(snapshot) ? snapshot.tree! : ''
  const key = from ? intervalKey(from, to) : ''
  const entry = useStore((s) => (key ? s.diffs[runID]?.intervals[key] : undefined))
  // Only its presence decides whether to ask; depending on the object would
  // re-run the effect on every write to it.
  const cached = entry !== undefined

  useEffect(() => {
    if (!runID || !key) return
    const store = useStore.getState()
    // The store, not the rendered entry: the write below is what stops a
    // second fetch, and only the store has it immediately.
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
